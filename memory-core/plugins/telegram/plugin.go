// Package telegram implements the Telegram source plugin.
//
// It connects to the Telegram Bot API via long-polling (getUpdates) and ingests
// private and group messages into the Memory Core event stream.
//
// Security notes:
//   - Messages from the bot itself are always filtered to prevent event loops.
//   - Commands can be filtered via allowedCommands to only ingest specific
//     interactions (e.g. /remember, /ask).
//   - If using webhooks instead of long-polling, always validate the
//     X-Telegram-Bot-Api-Secret-Token header against TELEGRAM_WEBHOOK_SECRET.
//   - The TELEGRAM_BOT_TOKEN must be kept secret; never log it.
package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/agent-memoryos/memory-core/event"
	"github.com/agent-memoryos/memory-core/plugins"
)

// Plugin implements the SourcePlugin interface for Telegram.
type Plugin struct {
	token           string
	engine          *event.Engine
	cancelFunc      context.CancelFunc
	httpClient      *http.Client
	allowedCommands map[string]bool // if non-empty, only messages starting with these commands are ingested
	lastUpdateID    int64
}

// New creates a new Telegram plugin.
// The first argument is the event engine used to ingest messages.
func New(engine *event.Engine) *Plugin {
	return &Plugin{
		token:      os.Getenv("TELEGRAM_BOT_TOKEN"),
		engine:     engine,
		httpClient: &http.Client{Timeout: 30 * time.Second},
		allowedCommands: map[string]bool{
			"/remember": true,
			"/ask":      true,
			"/note":     true,
			"/save":     true,
		},
	}
}

// WithToken sets the bot token explicitly (overrides TELEGRAM_BOT_TOKEN env var).
func (p *Plugin) WithToken(token string) *Plugin {
	p.token = token
	return p
}

// WithAllowedCommands sets which commands are ingested.
// Provide an empty/nil slice to ingest all messages.
func (p *Plugin) WithAllowedCommands(cmds []string) *Plugin {
	if len(cmds) == 0 {
		p.allowedCommands = nil // nil means allow all
		return p
	}
	p.allowedCommands = make(map[string]bool)
	for _, c := range cmds {
		p.allowedCommands[c] = true
	}
	return p
}

// Name returns the plugin identifier.
func (p *Plugin) Name() string { return "telegram" }

// Start connects to the Telegram Bot API via long-polling.
func (p *Plugin) Start(ctx context.Context) error {
	if p.token == "" {
		return fmt.Errorf("telegram: TELEGRAM_BOT_TOKEN not set")
	}

	ctx, p.cancelFunc = context.WithCancel(ctx)

	go p.poll(ctx)
	go p.watchShutdown(ctx)

	log.Println("telegram plugin: polling started")
	return nil
}

// Stop cancels the polling loop.
func (p *Plugin) Stop() error {
	if p.cancelFunc != nil {
		p.cancelFunc()
	}
	log.Println("telegram plugin: stopped")
	return nil
}

// Health checks connectivity by calling getMe.
func (p *Plugin) Health(ctx context.Context) error {
	if p.token == "" {
		return fmt.Errorf("telegram: TELEGRAM_BOT_TOKEN not set")
	}
	var resp struct {
		OK bool `json:"ok"`
	}
	if err := p.apiCall(ctx, "getMe", nil, &resp); err != nil {
		return fmt.Errorf("telegram: health check failed: %w", err)
	}
	if !resp.OK {
		return fmt.Errorf("telegram: api returned not ok")
	}
	return nil
}

// ---------------------------------------------------------------------------
// polling loop
// ---------------------------------------------------------------------------

func (p *Plugin) poll(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.fetchUpdates(ctx)
		}
	}
}

func (p *Plugin) fetchUpdates(ctx context.Context) {
	params := map[string]string{
		"timeout":         "3", // short timeout so we respond to ctx cancellation quickly
		"offset":          strconv.FormatInt(p.lastUpdateID+1, 10),
		"allowed_updates": `["message","edited_message","channel_post"]`,
	}

	var resp struct {
		OK     bool             `json:"ok"`
		Result []telegramUpdate `json:"result"`
	}

	if err := p.apiCall(ctx, "getUpdates", params, &resp); err != nil {
		if ctx.Err() == nil {
			log.Printf("telegram: poll error: %v", err)
		}
		return
	}

	for _, u := range resp.Result {
		p.lastUpdateID = u.UpdateID

		if u.Message != nil {
			p.handleMessage(ctx, u.Message)
		}
		if u.EditedMessage != nil {
			p.handleMessage(ctx, u.EditedMessage)
		}
		if u.ChannelPost != nil {
			p.handleMessage(ctx, u.ChannelPost)
		}
	}
}

// ---------------------------------------------------------------------------
// message handling
// ---------------------------------------------------------------------------

func (p *Plugin) handleMessage(ctx context.Context, m *telegramMessage) {
	// Security: ignore messages from the bot itself to prevent loops.
	if m.From != nil && m.From.IsBot {
		return
	}

	// Only handle text messages.
	if m.Text == "" {
		return
	}

	// If we have an allowed-command filter, check the message.
	if p.allowedCommands != nil {
		ok := false
		text := strings.TrimSpace(m.Text)
		for cmd := range p.allowedCommands {
			if strings.HasPrefix(text, cmd) {
				ok = true
				break
			}
		}
		if !ok {
			return
		}
	}

	// Derive user/session identifiers.
	userID := strconv.FormatInt(m.From.ID, 10)
	sessionID := strconv.FormatInt(m.Chat.ID, 10)

	// Build a human-readable text line including chat context.
	chatType := "private"
	if m.Chat.Type != "" {
		chatType = m.Chat.Type
	}
	text := fmt.Sprintf("[%s] %s: %s", chatType, userID, m.Text)

	_, err := plugins.IngestToEvent(
		ctx,
		p.engine,
		userID,    // user_id
		"",        // agent_id
		sessionID, // session_id = chat
		"telegram",
		text,
	)
	if err != nil {
		log.Printf("telegram: ingest error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// API helper
// ---------------------------------------------------------------------------

func (p *Plugin) apiCall(ctx context.Context, method string, params map[string]string, result interface{}) error {
	url := fmt.Sprintf("https://api.telegram.org/bot%s/%s", p.token, method)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}

	q := req.URL.Query()
	for k, v := range params {
		q.Add(k, v)
	}
	req.URL.RawQuery = q.Encode()

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("telegram api: %s: %s", resp.Status, string(body))
	}

	return json.Unmarshal(body, result)
}

// ---------------------------------------------------------------------------
// shutdown
// ---------------------------------------------------------------------------

func (p *Plugin) watchShutdown(ctx context.Context) {
	sc := make(chan os.Signal, 1)
	signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM)

	select {
	case <-ctx.Done():
	case <-sc:
	}
	_ = p.Stop()
}

// ---------------------------------------------------------------------------
// Telegram API types (minimal subset)
// ---------------------------------------------------------------------------

type telegramUpdate struct {
	UpdateID      int64            `json:"update_id"`
	Message       *telegramMessage `json:"message"`
	EditedMessage *telegramMessage `json:"edited_message"`
	ChannelPost   *telegramMessage `json:"channel_post"`
}

type telegramMessage struct {
	MessageID int64         `json:"message_id"`
	From      *telegramUser `json:"from"`
	Chat      telegramChat  `json:"chat"`
	Text      string        `json:"text"`
}

type telegramUser struct {
	ID       int64  `json:"id"`
	IsBot    bool   `json:"is_bot"`
	Username string `json:"username"`
}

type telegramChat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
}
