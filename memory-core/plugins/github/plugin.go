// Package github implements the GitHub Webhook source plugin.
//
// It starts a local HTTP server that receives GitHub webhook events,
// validates the HMAC signature, parses the payload, and ingests
// relevant events as memory.
//
// Supported events:
//   - issues (opened, edited, closed, reopened)
//   - pull_request (opened, edited, closed, reopened)
//   - push
//   - star (created, deleted)
//
// Security:
//   - Every request must include a valid X-Hub-Signature-256 header.
//   - The webhook secret is read from the GITHUB_WEBHOOK_SECRET env var.
//   - Only the signature algorithm sha256 is accepted.
package github

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/agent-memoryos/memory-core/event"
	"github.com/agent-memoryos/memory-core/plugins"
)

// Plugin implements the SourcePlugin interface for GitHub webhooks.
type Plugin struct {
	secret     string
	listenAddr string
	engine     *event.Engine

	server     *http.Server
	cancelFunc context.CancelFunc
	mu         sync.Mutex
}

// New creates a new GitHub webhook plugin.
func New(engine *event.Engine) *Plugin {
	addr := os.Getenv("GITHUB_WEBHOOK_ADDR")
	if addr == "" {
		addr = "0.0.0.0:8090"
	}
	return &Plugin{
		secret:     os.Getenv("GITHUB_WEBHOOK_SECRET"),
		listenAddr: addr,
		engine:     engine,
	}
}

// WithSecret sets the webhook secret explicitly.
func (p *Plugin) WithSecret(secret string) *Plugin {
	p.secret = secret
	return p
}

// WithAddr sets the listen address explicitly.
func (p *Plugin) WithAddr(addr string) *Plugin {
	p.listenAddr = addr
	return p
}

// Name returns the plugin identifier.
func (p *Plugin) Name() string { return "github" }

// Start begins listening for GitHub webhook events.
func (p *Plugin) Start(ctx context.Context) error {
	if p.secret == "" {
		return fmt.Errorf("github: GITHUB_WEBHOOK_SECRET not set")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/webhook", p.handleWebhook)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	p.server = &http.Server{
		Addr:         p.listenAddr,
		Handler:      mux,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
	}

	ctx, p.cancelFunc = context.WithCancel(ctx)

	go func() {
		log.Printf("github plugin: listening on %s", p.listenAddr)
		if err := p.server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("github: server error: %v", err)
		}
	}()

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		p.server.Shutdown(shutdownCtx)
	}()

	return nil
}

// Stop gracefully shuts down the HTTP server.
func (p *Plugin) Stop() error {
	if p.cancelFunc != nil {
		p.cancelFunc()
	}
	if p.server != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return p.server.Shutdown(shutdownCtx)
	}
	return nil
}

// Health checks if the server is reachable.
func (p *Plugin) Health(ctx context.Context) error {
	host, port, err := net.SplitHostPort(p.listenAddr)
	if err != nil {
		return fmt.Errorf("github: invalid listen address: %w", err)
	}
	if host == "" {
		host = "127.0.0.1"
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(host, port), 2*time.Second)
	if err != nil {
		return fmt.Errorf("github: server not reachable: %w", err)
	}
	conn.Close()
	return nil
}

// ---------------------------------------------------------------------------
// HTTP handler
// ---------------------------------------------------------------------------

func (p *Plugin) handleWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	eventType := r.Header.Get("X-GitHub-Event")
	if eventType == "" {
		http.Error(w, "missing X-GitHub-Event header", http.StatusBadRequest)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.Printf("github: read body error: %v", err)
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	// Validate HMAC signature.
	sig := r.Header.Get("X-Hub-Signature-256")
	if !p.verifySignature(body, sig) {
		log.Printf("github: invalid signature for event=%s", eventType)
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	// Parse and ingest the event.
	text, userID := p.formatEvent(eventType, body)
	if text == "" {
		// Unsupported or ignored event — still return 200 so GitHub
		// doesn't retry.
		w.WriteHeader(http.StatusOK)
		return
	}

	ctx := r.Context()
	_, err = plugins.IngestToEvent(
		ctx,
		p.engine,
		userID,
		"",
		eventType,
		"github",
		text,
	)
	if err != nil {
		log.Printf("github: ingest error: %v", err)
	}

	w.WriteHeader(http.StatusOK)
}

func (p *Plugin) verifySignature(body []byte, sigHeader string) bool {
	if sigHeader == "" {
		return false
	}
	const prefix = "sha256="
	if !strings.HasPrefix(sigHeader, prefix) {
		return false
	}
	gotMAC, err := hex.DecodeString(sigHeader[len(prefix):])
	if err != nil {
		return false
	}

	mac := hmac.New(sha256.New, []byte(p.secret))
	mac.Write(body)
	expectedMAC := mac.Sum(nil)

	return hmac.Equal(gotMAC, expectedMAC)
}

// ---------------------------------------------------------------------------
// Event formatting
// ---------------------------------------------------------------------------

// formatEvent parses the webhook body and returns (formatted_text, user_id).
// Returns empty text for unsupported or ignored events.
func (p *Plugin) formatEvent(eventType string, body []byte) (string, string) {
	switch eventType {
	case "issues":
		return p.formatIssueEvent(body)
	case "pull_request":
		return p.formatPullRequestEvent(body)
	case "push":
		return p.formatPushEvent(body)
	case "star":
		return p.formatStarEvent(body)
	default:
		return "", ""
	}
}

// --- Issues ---

type issuePayload struct {
	Action     string `json:"action"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	Issue struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		Body   string `json:"body"`
		State  string `json:"state"`
		User   struct {
			Login string `json:"login"`
		} `json:"user"`
		HTMLURL string `json:"html_url"`
	} `json:"issue"`
	Sender struct {
		Login string `json:"login"`
	} `json:"sender"`
}

func (p *Plugin) formatIssueEvent(body []byte) (string, string) {
	var evt issuePayload
	if err := json.Unmarshal(body, &evt); err != nil {
		log.Printf("github: parse issues error: %v", err)
		return "", ""
	}

	action := evt.Action
	if action != "opened" && action != "edited" && action != "closed" && action != "reopened" {
		return "", ""
	}

	text := fmt.Sprintf(
		"[Issue %s] %s #%d: %s\nRepo: %s\nBy: %s\nURL: %s\n\n%s",
		action,
		evt.Repository.FullName,
		evt.Issue.Number,
		evt.Issue.Title,
		evt.Repository.FullName,
		evt.Sender.Login,
		evt.Issue.HTMLURL,
		evt.Issue.Body,
	)

	return text, evt.Sender.Login
}

// --- Pull Requests ---

type pullRequestPayload struct {
	Action     string `json:"action"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	PullRequest struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
		Body   string `json:"body"`
		State  string `json:"state"`
		Merged bool   `json:"merged"`
		User   struct {
			Login string `json:"login"`
		} `json:"user"`
		HTMLURL string `json:"html_url"`
	} `json:"pull_request"`
	Sender struct {
		Login string `json:"login"`
	} `json:"sender"`
}

func (p *Plugin) formatPullRequestEvent(body []byte) (string, string) {
	var evt pullRequestPayload
	if err := json.Unmarshal(body, &evt); err != nil {
		log.Printf("github: parse pull_request error: %v", err)
		return "", ""
	}

	action := evt.Action
	if action != "opened" && action != "edited" && action != "closed" && action != "reopened" {
		return "", ""
	}

	// Add merge info for closed PRs.
	extra := ""
	if action == "closed" && evt.PullRequest.Merged {
		extra = " (merged)"
	}

	text := fmt.Sprintf(
		"[PR %s%s] %s #%d: %s\nRepo: %s\nBy: %s\nURL: %s\n\n%s",
		action,
		extra,
		evt.Repository.FullName,
		evt.PullRequest.Number,
		evt.PullRequest.Title,
		evt.Repository.FullName,
		evt.Sender.Login,
		evt.PullRequest.HTMLURL,
		evt.PullRequest.Body,
	)

	return text, evt.Sender.Login
}

// --- Push ---

type pushPayload struct {
	Ref        string `json:"ref"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	Pusher struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	} `json:"pusher"`
	Commits []struct {
		ID      string `json:"id"`
		Message string `json:"message"`
		Author  struct {
			Name  string `json:"name"`
			Email string `json:"email"`
		} `json:"author"`
		URL string `json:"url"`
	} `json:"commits"`
	Sender struct {
		Login string `json:"login"`
	} `json:"sender"`
}

func (p *Plugin) formatPushEvent(body []byte) (string, string) {
	var evt pushPayload
	if err := json.Unmarshal(body, &evt); err != nil {
		log.Printf("github: parse push error: %v", err)
		return "", ""
	}

	branch := strings.TrimPrefix(evt.Ref, "refs/heads/")

	var b strings.Builder
	b.WriteString(fmt.Sprintf("[Push] %s → %s\n", evt.Repository.FullName, branch))
	b.WriteString(fmt.Sprintf("Pusher: %s <%s>\n", evt.Pusher.Name, evt.Pusher.Email))
	b.WriteString(fmt.Sprintf("Commits: %d\n\n", len(evt.Commits)))

	for i, c := range evt.Commits {
		if i >= 10 {
			b.WriteString(fmt.Sprintf("... and %d more commits\n", len(evt.Commits)-10))
			break
		}
		shortID := c.ID
		if len(shortID) > 7 {
			shortID = shortID[:7]
		}
		msg := strings.TrimSpace(c.Message)
		if idx := strings.IndexByte(msg, '\n'); idx != -1 {
			msg = msg[:idx]
		}
		b.WriteString(fmt.Sprintf("  %s: %s (%s)\n", shortID, msg, c.Author.Name))
	}

	return b.String(), evt.Sender.Login
}

// --- Star ---

type starPayload struct {
	Action     string `json:"action"`
	Repository struct {
		FullName string `json:"full_name"`
	} `json:"repository"`
	Sender struct {
		Login string `json:"login"`
	} `json:"sender"`
	StarredAt string `json:"starred_at"`
}

func (p *Plugin) formatStarEvent(body []byte) (string, string) {
	var evt starPayload
	if err := json.Unmarshal(body, &evt); err != nil {
		log.Printf("github: parse star error: %v", err)
		return "", ""
	}

	action := evt.Action
	if action == "created" || action == "deleted" {
		text := fmt.Sprintf("[Star %s] %s\nRepo: %s\nUser: %s\nAt: %s",
			evt.Action,
			evt.Repository.FullName,
			evt.Repository.FullName,
			evt.Sender.Login,
			evt.StarredAt,
		)
		return text, evt.Sender.Login
	}

	return "", ""
}
