// Package notion implements the Notion source plugin.
//
// It periodically polls the Notion API for recently updated pages in a
// specified database and ingests the page content as memory events.
//
// Prerequisites:
//   - A Notion integration with access to the target database.
//   - NOTION_API_TOKEN set to your integration secret.
//   - NOTION_DATABASE_ID set to the UUID of the database to monitor.
//
// Security:
//   - The API token is read from NOTION_API_TOKEN env var only.
//   - Rate limiting is respected (max 3 requests per second).
//   - Only page titles and text content are ingested; file attachments
//     are not downloaded.
package notion

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/agent-memoryos/memory-core/event"
	"github.com/agent-memoryos/memory-core/plugins"
)

const notionAPIBase = "https://api.notion.com/v1"

// API version header value.
const notionVersion = "2022-06-28"

// Plugin implements the SourcePlugin interface for Notion.
type Plugin struct {
	apiToken    string
	databaseID  string
	pollSeconds int
	engine      *event.Engine

	client       *http.Client
	cancelFunc   context.CancelFunc
	lastPolledAt time.Time

	// rate limiter — max 3 requests per second.
	rateLimiter *time.Ticker
	mu          sync.Mutex
}

// New creates a new Notion plugin.
func New(engine *event.Engine) *Plugin {
	pollSec := 60
	if v := os.Getenv("NOTION_POLL_SECONDS"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			pollSec = n
		}
	}
	return &Plugin{
		apiToken:    os.Getenv("NOTION_API_TOKEN"),
		databaseID:  os.Getenv("NOTION_DATABASE_ID"),
		pollSeconds: pollSec,
		engine:      engine,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		rateLimiter: time.NewTicker(time.Second / 3),
	}
}

// WithToken sets the API token explicitly.
func (p *Plugin) WithToken(token string) *Plugin {
	p.apiToken = token
	return p
}

// WithDatabaseID sets the database ID explicitly.
func (p *Plugin) WithDatabaseID(id string) *Plugin {
	p.databaseID = id
	return p
}

// WithPollSeconds sets the polling interval in seconds.
func (p *Plugin) WithPollSeconds(sec int) *Plugin {
	if sec > 0 {
		p.pollSeconds = sec
	}
	return p
}

// Name returns the plugin identifier.
func (p *Plugin) Name() string { return "notion" }

// Start begins polling the Notion API.
func (p *Plugin) Start(ctx context.Context) error {
	if p.apiToken == "" {
		return fmt.Errorf("notion: NOTION_API_TOKEN not set")
	}
	if p.databaseID == "" {
		return fmt.Errorf("notion: NOTION_DATABASE_ID not set")
	}

	ctx, p.cancelFunc = context.WithCancel(ctx)

	// Verify connectivity on start.
	if err := p.verifyConnection(ctx); err != nil {
		return fmt.Errorf("notion: connection check failed: %w", err)
	}

	go p.pollLoop(ctx)

	log.Println("notion plugin: polling every", p.pollSeconds, "seconds")
	return nil
}

// Stop cancels the polling loop.
func (p *Plugin) Stop() error {
	if p.cancelFunc != nil {
		p.cancelFunc()
	}
	if p.rateLimiter != nil {
		p.rateLimiter.Stop()
	}
	log.Println("notion plugin: stopped")
	return nil
}

// Health verifies the Notion API is reachable.
func (p *Plugin) Health(ctx context.Context) error {
	return p.verifyConnection(ctx)
}

// ---------------------------------------------------------------------------
// Polling loop
// ---------------------------------------------------------------------------

func (p *Plugin) pollLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Duration(p.pollSeconds) * time.Second)
	defer ticker.Stop()

	// Do an initial fetch.
	p.pollOnce(ctx)

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.pollOnce(ctx)
		}
	}
}

func (p *Plugin) pollOnce(ctx context.Context) {
	now := time.Now().UTC()

	// Query database for recently updated pages.
	pages, err := p.queryDatabase(ctx, p.lastPolledAt)
	if err != nil {
		log.Printf("notion: query database error: %v", err)
		return
	}

	for _, page := range pages {
		content, err := p.fetchPageContent(ctx, page.ID)
		if err != nil {
			log.Printf("notion: fetch page %s error: %v", page.ID, err)
			continue
		}

		text := p.formatPage(page, content)

		_, err = plugins.IngestToEvent(
			ctx,
			p.engine,
			"notion",
			"",
			p.databaseID,
			"notion",
			text,
		)
		if err != nil {
			log.Printf("notion: ingest error: %v", err)
		}
	}

	p.lastPolledAt = now
}

// ---------------------------------------------------------------------------
// Notion API helpers
// ---------------------------------------------------------------------------

// notionPageSummary holds the result of a database query.
type notionPageSummary struct {
	ID         string
	Title      string
	URL        string
	CreatedAt  string
	UpdatedAt  string
	Properties map[string]string
}

// queryDatabase fetches pages updated after the given time.
func (p *Plugin) queryDatabase(ctx context.Context, after time.Time) ([]notionPageSummary, error) {
	p.rateLimit()

	url := fmt.Sprintf("%s/databases/%s/query", notionAPIBase, p.databaseID)

	body := map[string]interface{}{
		"page_size": 20,
		"sorts": []map[string]interface{}{
			{
				"timestamp": "last_edited_time",
				"direction": "descending",
			},
		},
	}

	if !after.IsZero() {
		body["filter"] = map[string]interface{}{
			"timestamp": "last_edited_time",
			"last_edited_time": map[string]string{
				"on_or_after": after.Format(time.RFC3339),
			},
		}
	}

	var result struct {
		Results []struct {
			ID         string `json:"id"`
			URL        string `json:"url"`
			CreatedAt  string `json:"created_time"`
			UpdatedAt  string `json:"last_edited_time"`
			Properties map[string]struct {
				Type  string `json:"type"`
				Title []struct {
					PlainText string `json:"plain_text"`
				} `json:"title"`
				RichText []struct {
					PlainText string `json:"plain_text"`
				} `json:"rich_text"`
			} `json:"properties"`
		} `json:"results"`
	}

	if err := p.doRequest(ctx, "POST", url, body, &result); err != nil {
		return nil, err
	}

	pages := make([]notionPageSummary, 0, len(result.Results))
	for _, r := range result.Results {
		props := make(map[string]string)
		title := "Untitled"

		for name, prop := range r.Properties {
			switch prop.Type {
			case "title":
				if len(prop.Title) > 0 {
					title = prop.Title[0].PlainText
				}
				props[name] = title
			case "rich_text":
				var texts []string
				for _, rt := range prop.RichText {
					texts = append(texts, rt.PlainText)
				}
				props[name] = strings.Join(texts, " ")
			}
		}

		pages = append(pages, notionPageSummary{
			ID:         r.ID,
			Title:      title,
			URL:        r.URL,
			CreatedAt:  r.CreatedAt,
			UpdatedAt:  r.UpdatedAt,
			Properties: props,
		})
	}

	return pages, nil
}

// fetchPageContent retrieves the block children (text content) of a page.
func (p *Plugin) fetchPageContent(ctx context.Context, pageID string) (string, error) {
	p.rateLimit()

	url := fmt.Sprintf("%s/blocks/%s/children?page_size=50", notionAPIBase, pageID)

	var result struct {
		Results    []notionBlock `json:"results"`
		HasMore    bool          `json:"has_more"`
		NextCursor string        `json:"next_cursor"`
	}

	if err := p.doRequest(ctx, "GET", url, nil, &result); err != nil {
		return "", err
	}

	var texts []string
	for _, block := range result.Results {
		text := extractBlockText(block)
		if text != "" {
			texts = append(texts, text)
		}
	}

	return strings.Join(texts, "\n"), nil
}

// notionBlock represents a Notion block object.
type notionBlock struct {
	Type      string `json:"type"`
	Paragraph struct {
		RichText []notionRichText `json:"rich_text"`
	} `json:"paragraph"`
	Heading1 struct {
		RichText []notionRichText `json:"rich_text"`
	} `json:"heading_1"`
	Heading2 struct {
		RichText []notionRichText `json:"rich_text"`
	} `json:"heading_2"`
	Heading3 struct {
		RichText []notionRichText `json:"rich_text"`
	} `json:"heading_3"`
	BulletedListItem struct {
		RichText []notionRichText `json:"rich_text"`
	} `json:"bulleted_list_item"`
	NumberedListItem struct {
		RichText []notionRichText `json:"rich_text"`
	} `json:"numbered_list_item"`
	ToDo struct {
		RichText []notionRichText `json:"rich_text"`
		Checked  bool             `json:"checked"`
	} `json:"to_do"`
	Code struct {
		RichText []notionRichText `json:"rich_text"`
		Language string           `json:"language"`
	} `json:"code"`
	Quote struct {
		RichText []notionRichText `json:"rich_text"`
	} `json:"quote"`
	Callout struct {
		RichText []notionRichText `json:"rich_text"`
	} `json:"callout"`
	Toggle struct {
		RichText []notionRichText `json:"rich_text"`
	} `json:"toggle"`
}

type notionRichText struct {
	PlainText string `json:"plain_text"`
}

// extractBlockText returns the plain text content of a block.
func extractBlockText(block notionBlock) string {
	getText := func(rt []notionRichText) string {
		var texts []string
		for _, t := range rt {
			texts = append(texts, t.PlainText)
		}
		return strings.Join(texts, "")
	}

	switch block.Type {
	case "paragraph":
		return getText(block.Paragraph.RichText)
	case "heading_1":
		return "# " + getText(block.Heading1.RichText)
	case "heading_2":
		return "## " + getText(block.Heading2.RichText)
	case "heading_3":
		return "### " + getText(block.Heading3.RichText)
	case "bulleted_list_item":
		return "- " + getText(block.BulletedListItem.RichText)
	case "numbered_list_item":
		return "1. " + getText(block.NumberedListItem.RichText)
	case "to_do":
		checked := " "
		if block.ToDo.Checked {
			checked = "x"
		}
		return fmt.Sprintf("- [%s] %s", checked, getText(block.ToDo.RichText))
	case "code":
		return fmt.Sprintf("```%s\n%s\n```", block.Code.Language, getText(block.Code.RichText))
	case "quote":
		return "> " + getText(block.Quote.RichText)
	case "callout":
		return "💡 " + getText(block.Callout.RichText)
	case "toggle":
		return "▶ " + getText(block.Toggle.RichText)
	default:
		return ""
	}
}

// ---------------------------------------------------------------------------
// Formatting
// ---------------------------------------------------------------------------

func (p *Plugin) formatPage(page notionPageSummary, content string) string {
	var b strings.Builder

	b.WriteString(fmt.Sprintf("# %s\n", page.Title))
	b.WriteString(fmt.Sprintf("URL: %s\n", page.URL))
	b.WriteString(fmt.Sprintf("Created: %s\n", page.CreatedAt))
	b.WriteString(fmt.Sprintf("Updated: %s\n\n", page.UpdatedAt))

	// Include relevant properties.
	for key, val := range page.Properties {
		if val != "" && val != page.Title {
			b.WriteString(fmt.Sprintf("%s: %s\n", key, val))
		}
	}

	b.WriteString("\n")
	b.WriteString(content)

	return b.String()
}

// ---------------------------------------------------------------------------
// HTTP helpers
// ---------------------------------------------------------------------------

func (p *Plugin) verifyConnection(ctx context.Context) error {
	// Simple API check: list users to verify token.
	p.rateLimit()
	url := fmt.Sprintf("%s/users/me", notionAPIBase)
	var result struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	return p.doRequest(ctx, "GET", url, nil, &result)
}

func (p *Plugin) doRequest(ctx context.Context, method, url string, body interface{}, result interface{}) error {
	var reqBody io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("marshal request: %w", err)
		}
		reqBody = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}

	req.Header.Set("Authorization", "Bearer "+p.apiToken)
	req.Header.Set("Notion-Version", notionVersion)
	req.Header.Set("Content-Type", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("http request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("notion API error %d: %s", resp.StatusCode, string(respBody))
	}

	if result != nil {
		if err := json.NewDecoder(resp.Body).Decode(result); err != nil {
			return fmt.Errorf("decode response: %w", err)
		}
	}

	return nil
}

// rateLimit waits for the rate limiter tick.
func (p *Plugin) rateLimit() {
	<-p.rateLimiter.C
}
