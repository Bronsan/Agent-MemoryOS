// Package obsidian implements the Obsidian vault source plugin.
//
// It watches a local Obsidian vault directory for changes to Markdown (.md)
// files using fsnotify. When a file is created or modified, the plugin
// parses its YAML frontmatter (if present) and content, then ingests the
// result as a memory event.
//
// Prerequisites:
//   - An existing Obsidian vault directory.
//   - The filesystem must support fsnotify (Linux inotify, macOS FSEvents,
//     Windows ReadDirectoryChangesW).
//
// Security:
//   - Only files with .md extension are processed.
//   - Hidden files and directories (starting with '.') are skipped.
//   - The vault path is read from OBSIDIAN_VAULT_PATH env var.
package obsidian

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/agent-memoryos/memory-core/event"
	"github.com/agent-memoryos/memory-core/plugins"
	"github.com/fsnotify/fsnotify"
)

// Plugin implements the SourcePlugin interface for Obsidian.
type Plugin struct {
	vaultPath  string
	engine     *event.Engine
	watcher    *fsnotify.Watcher
	cancelFunc context.CancelFunc
	mu         sync.Mutex
}

// New creates a new Obsidian vault plugin.
func New(engine *event.Engine) *Plugin {
	return &Plugin{
		vaultPath: os.Getenv("OBSIDIAN_VAULT_PATH"),
		engine:    engine,
	}
}

// WithVaultPath sets the vault path explicitly.
func (p *Plugin) WithVaultPath(path string) *Plugin {
	p.vaultPath = path
	return p
}

// Name returns the plugin identifier.
func (p *Plugin) Name() string { return "obsidian" }

// Start begins watching the vault directory.
func (p *Plugin) Start(ctx context.Context) error {
	if p.vaultPath == "" {
		return fmt.Errorf("obsidian: OBSIDIAN_VAULT_PATH not set")
	}

	info, err := os.Stat(p.vaultPath)
	if err != nil {
		return fmt.Errorf("obsidian: vault path error: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("obsidian: vault path is not a directory: %s", p.vaultPath)
	}

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("obsidian: create watcher: %w", err)
	}

	// Add the vault root and all subdirectories.
	if err := p.addRecursive(watcher, p.vaultPath); err != nil {
		watcher.Close()
		return fmt.Errorf("obsidian: add watcher: %w", err)
	}

	p.watcher = watcher
	ctx, p.cancelFunc = context.WithCancel(ctx)

	go p.watchLoop(ctx)

	log.Println("obsidian plugin: watching", p.vaultPath)
	return nil
}

// Stop closes the file watcher.
func (p *Plugin) Stop() error {
	if p.cancelFunc != nil {
		p.cancelFunc()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.watcher != nil {
		err := p.watcher.Close()
		p.watcher = nil
		return err
	}
	return nil
}

// Health checks if the watcher is alive.
func (p *Plugin) Health(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.watcher == nil {
		return fmt.Errorf("obsidian: not watching")
	}
	// Check if vault path still exists.
	if _, err := os.Stat(p.vaultPath); err != nil {
		return fmt.Errorf("obsidian: vault path error: %w", err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Watch loop
// ---------------------------------------------------------------------------

func (p *Plugin) watchLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case evt, ok := <-p.watcher.Events:
			if !ok {
				return
			}
			p.handleEvent(ctx, evt)
		case err, ok := <-p.watcher.Errors:
			if !ok {
				return
			}
			log.Printf("obsidian: watcher error: %v", err)
		}
	}
}

func (p *Plugin) handleEvent(ctx context.Context, evt fsnotify.Event) {
	// Only process .md files.
	if filepath.Ext(evt.Name) != ".md" {
		return
	}

	// Skip hidden files and directories.
	if isHidden(evt.Name) {
		return
	}

	switch {
	case evt.Has(fsnotify.Create):
		// When a new directory is created, add it to the watcher.
		if info, err := os.Stat(evt.Name); err == nil && info.IsDir() {
			if !isHidden(evt.Name) {
				p.mu.Lock()
				if p.watcher != nil {
					_ = p.watcher.Add(evt.Name)
				}
				p.mu.Unlock()
			}
			return
		}
		p.ingestFile(ctx, evt.Name, "created")

	case evt.Has(fsnotify.Write):
		p.ingestFile(ctx, evt.Name, "modified")
	}
}

// ---------------------------------------------------------------------------
// File parsing
// ---------------------------------------------------------------------------

// frontmatter represents parsed YAML frontmatter from a Markdown file.
type frontmatter map[string]string

// parseFile reads a .md file, extracts frontmatter and content, and returns
// a formatted text suitable for ingestion.
func (p *Plugin) parseFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	fm, content, err := splitFrontmatter(file)
	if err != nil {
		return "", err
	}

	relPath, _ := filepath.Rel(p.vaultPath, path)
	title := strings.TrimSuffix(filepath.Base(path), ".md")

	var b strings.Builder

	// Use title from frontmatter if present, otherwise fall back to filename.
	if t, ok := fm["title"]; ok && t != "" {
		b.WriteString(fmt.Sprintf("# %s\n", t))
	} else {
		b.WriteString(fmt.Sprintf("# %s\n", title))
	}

	b.WriteString(fmt.Sprintf("File: %s\n", relPath))

	// Include relevant frontmatter fields.
	tags := fm["tags"]
	if tags != "" {
		b.WriteString(fmt.Sprintf("Tags: %s\n", tags))
	}
	created := fm["created"] // or "date"
	if created == "" {
		created = fm["date"]
	}
	if created != "" {
		b.WriteString(fmt.Sprintf("Created: %s\n", created))
	}
	updated := fm["updated"]
	if updated != "" {
		b.WriteString(fmt.Sprintf("Updated: %s\n", updated))
	}

	b.WriteString("\n")
	b.WriteString(strings.TrimSpace(content))

	return b.String(), nil
}

// splitFrontmatter reads a markdown file and separates YAML frontmatter
// (delimited by ---) from the body content.
func splitFrontmatter(f *os.File) (frontmatter, string, error) {
	fm := make(frontmatter)
	scanner := bufio.NewScanner(f)

	// Check if the first line starts a frontmatter block.
	if !scanner.Scan() {
		return fm, "", nil // empty file
	}
	firstLine := scanner.Text()
	if strings.TrimSpace(firstLine) != "---" {
		// No frontmatter — the entire file is content.
		var body strings.Builder
		body.WriteString(firstLine)
		body.WriteByte('\n')
		for scanner.Scan() {
			body.WriteString(scanner.Text())
			body.WriteByte('\n')
		}
		return fm, body.String(), nil
	}

	// Parse frontmatter lines until closing ---.
	inFM := true
	for scanner.Scan() {
		line := scanner.Text()
		if inFM {
			if strings.TrimSpace(line) == "---" {
				inFM = false
				continue
			}
			// Simple YAML-ish key: value parser.
			if idx := strings.IndexByte(line, ':'); idx > 0 {
				key := strings.TrimSpace(line[:idx])
				val := strings.TrimSpace(line[idx+1:])
				// Remove surrounding quotes.
				val = strings.Trim(val, `"'`)
				fm[key] = val
			}
		}
	}
	// Note: the scanner already consumed the file. We need to re-read the
	// content after the frontmatter. Re-open the file.

	_, err := f.Seek(0, 0)
	if err != nil {
		return fm, "", err
	}
	scanner2 := bufio.NewScanner(f)
	var content strings.Builder
	inContent := false
	fmClosed := false
	for scanner2.Scan() {
		line := scanner2.Text()
		if !fmClosed {
			if strings.TrimSpace(line) == "---" {
				if inContent {
					fmClosed = true
					continue
				}
				inContent = true
				continue
			}
			if inContent {
				// Still inside frontmatter.
				continue
			}
			// First --- not yet encountered; skip.
			continue
		}
		content.WriteString(line)
		content.WriteByte('\n')
	}

	return fm, content.String(), nil
}

// ---------------------------------------------------------------------------
// Ingestion
// ---------------------------------------------------------------------------

func (p *Plugin) ingestFile(ctx context.Context, path string, action string) {
	text, err := p.parseFile(path)
	if err != nil {
		log.Printf("obsidian: parse %s error: %v", path, err)
		return
	}

	relPath, _ := filepath.Rel(p.vaultPath, path)

	formatted := fmt.Sprintf("[Obsidian %s] %s\n\n%s", action, relPath, text)

	_, err = plugins.IngestToEvent(
		ctx,
		p.engine,
		"obsidian",
		"",
		filepath.Base(filepath.Dir(path)),
		"obsidian",
		formatted,
	)
	if err != nil {
		log.Printf("obsidian: ingest error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// addRecursive adds path and all its subdirectories to the watcher.
// Hidden directories (starting with '.') are skipped.
func (p *Plugin) addRecursive(watcher *fsnotify.Watcher, root string) error {
	return filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // skip inaccessible paths
		}
		if !info.IsDir() {
			return nil
		}
		if isHidden(path) && path != root {
			return filepath.SkipDir
		}
		return watcher.Add(path)
	})
}

// isHidden returns true if any component of the path starts with a dot.
func isHidden(path string) bool {
	// Check individual components.
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if strings.HasPrefix(part, ".") && part != "." && part != ".." {
			return true
		}
	}
	return false
}
