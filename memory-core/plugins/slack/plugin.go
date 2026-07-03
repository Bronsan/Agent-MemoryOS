// Package slack implements the Slack source plugin.
//
// It connects via Slack Socket Mode (WebSocket) using a bot token and
// app-level token. Messages from channels and DMs, as well as app_mention
// events, are ingested into Memory Core.
//
// Prerequisites:
//   - Create a Slack App with Socket Mode enabled.
//   - Grant scopes: app_mentions:read, channels:history, groups:history,
//     im:history, mpim:history.
//   - Generate an app-level token with connections:write scope.
//
// Security notes:
//   - The socket-mode connection is authenticated via the app-level token.
//   - Messages from bots (including the app itself) are always skipped to
//     prevent event loops.
//   - The SLACK_BOT_TOKEN and SLACK_APP_TOKEN must be kept secret;
//     never log or commit them.
package slack

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/agent-memoryos/memory-core/event"
	"github.com/agent-memoryos/memory-core/plugins"
	"github.com/gorilla/websocket"
)

// Plugin implements the SourcePlugin interface for Slack.
type Plugin struct {
	botToken   string
	appToken   string
	engine     *event.Engine
	cancelFunc context.CancelFunc
	conn       *websocket.Conn
	connMu     sync.Mutex
}

// New creates a new Slack plugin.
// The first argument is the event engine used to ingest messages.
func New(engine *event.Engine) *Plugin {
	return &Plugin{
		botToken: os.Getenv("SLACK_BOT_TOKEN"),
		appToken: os.Getenv("SLACK_APP_TOKEN"),
		engine:   engine,
	}
}

// WithBotToken sets the bot token explicitly (overrides SLACK_BOT_TOKEN env var).
func (p *Plugin) WithBotToken(token string) *Plugin {
	p.botToken = token
	return p
}

// WithAppToken sets the app-level token explicitly (overrides SLACK_APP_TOKEN env var).
func (p *Plugin) WithAppToken(token string) *Plugin {
	p.appToken = token
	return p
}

// Name returns the plugin identifier.
func (p *Plugin) Name() string { return "slack" }

// Start connects to Slack Socket Mode and begins ingesting events.
func (p *Plugin) Start(ctx context.Context) error {
	if p.botToken == "" {
		return fmt.Errorf("slack: SLACK_BOT_TOKEN not set")
	}
	if p.appToken == "" {
		return fmt.Errorf("slack: SLACK_APP_TOKEN not set")
	}

	ctx, p.cancelFunc = context.WithCancel(ctx)

	go p.connectLoop(ctx)
	go p.watchShutdown(ctx)

	log.Println("slack plugin: socket mode connecting")
	return nil
}

// Stop closes the WebSocket connection.
func (p *Plugin) Stop() error {
	if p.cancelFunc != nil {
		p.cancelFunc()
	}
	p.connMu.Lock()
	defer p.connMu.Unlock()
	if p.conn != nil {
		// Send a clean close frame (best effort).
		_ = p.conn.WriteMessage(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
		_ = p.conn.Close()
		p.conn = nil
	}
	log.Println("slack plugin: stopped")
	return nil
}

// Health checks whether the Socket Mode connection is alive.
func (p *Plugin) Health(ctx context.Context) error {
	if p.conn == nil {
		return fmt.Errorf("slack: not connected")
	}
	return nil
}

// ---------------------------------------------------------------------------
// Socket Mode connection loop
// ---------------------------------------------------------------------------

func (p *Plugin) connectLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		wsURL, err := p.doAppsConnectionsOpen(ctx)
		if err != nil {
			log.Printf("slack: apps.connections.open error: %v", err)
			p.sleep(ctx, 5)
			continue
		}

		conn, _, err := websocket.DefaultDialer.DialContext(ctx, wsURL, nil)
		if err != nil {
			log.Printf("slack: websocket dial error: %v", err)
			p.sleep(ctx, 5)
			continue
		}

		p.connMu.Lock()
		p.conn = conn
		p.connMu.Unlock()

		log.Println("slack plugin: socket mode connected")
		p.readLoop(ctx, conn)

		// readLoop returned — reconnect after a short delay.
		p.connMu.Lock()
		p.conn = nil
		p.connMu.Unlock()

		p.sleep(ctx, 3)
	}
}

func (p *Plugin) readLoop(ctx context.Context, conn *websocket.Conn) {
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			if ctx.Err() == nil {
				log.Printf("slack: websocket read error: %v", err)
			}
			return
		}

		p.dispatchEvent(ctx, raw)
	}
}

// ---------------------------------------------------------------------------
// Slack Socket Mode: apps.connections.open
//
// Calls POST https://slack.com/api/apps.connections.open with the app-level
// token to obtain a WebSocket URL. This uses net/http directly — no extra
// Slack SDK dependency.
// ---------------------------------------------------------------------------

func (p *Plugin) doAppsConnectionsOpen(ctx context.Context) (string, error) {
	form := url.Values{}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		"https://slack.com/api/apps.connections.open",
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+p.appToken)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)

	var result struct {
		OK    bool   `json:"ok"`
		URL   string `json:"url"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return "", fmt.Errorf("slack: parse apps.connections.open response: %w", err)
	}
	if !result.OK {
		return "", fmt.Errorf("slack: apps.connections.open: %s", result.Error)
	}
	return result.URL, nil
}

// ---------------------------------------------------------------------------
// event dispatch
// ---------------------------------------------------------------------------

// slackSocketEvent is the envelope Slack sends over Socket Mode.
type slackSocketEvent struct {
	Type       string          `json:"type"`
	Payload    json.RawMessage `json:"payload"`
	EnvelopeID string          `json:"envelope_id"`
}

// slackEventPayload is the inner payload for "events_api" type.
type slackEventPayload struct {
	Event  json.RawMessage `json:"event"`
	TeamID string          `json:"team_id"`
}

// slackMessageEvent covers message and app_mention callbacks.
type slackMessageEvent struct {
	Type    string `json:"type"`
	User    string `json:"user"`
	BotID   string `json:"bot_id"`
	Text    string `json:"text"`
	Channel string `json:"channel"`
}

func (p *Plugin) dispatchEvent(ctx context.Context, raw []byte) {
	var envelope slackSocketEvent
	if err := json.Unmarshal(raw, &envelope); err != nil {
		log.Printf("slack: unmarshal envelope error: %v", err)
		return
	}

	// Acknowledge the envelope (Slack requires this for Socket Mode).
	if envelope.EnvelopeID != "" {
		ack := map[string]string{"envelope_id": envelope.EnvelopeID}
		ackRaw, _ := json.Marshal(ack)
		p.connMu.Lock()
		if p.conn != nil {
			_ = p.conn.WriteMessage(websocket.TextMessage, ackRaw)
		}
		p.connMu.Unlock()
	}

	switch envelope.Type {
	case "events_api":
		var payload slackEventPayload
		if err := json.Unmarshal(envelope.Payload, &payload); err != nil {
			log.Printf("slack: unmarshal payload error: %v", err)
			return
		}

		var evt slackMessageEvent
		if err := json.Unmarshal(payload.Event, &evt); err != nil {
			// Not a message event — could be url_verification, etc.
			return
		}

		switch evt.Type {
		case "message", "app_mention":
			p.handleMessage(ctx, evt)
		}
	}
}

func (p *Plugin) handleMessage(ctx context.Context, evt slackMessageEvent) {
	// Security: skip bot messages to prevent loops.
	if evt.BotID != "" {
		return
	}
	if evt.User == "" || evt.User == "USLACKBOT" {
		return
	}

	// Ignore empty messages.
	if evt.Text == "" {
		return
	}

	_, err := plugins.IngestToEvent(
		ctx,
		p.engine,
		evt.User,    // user_id
		"",          // agent_id
		evt.Channel, // session_id = channel / DM
		"slack",
		evt.Text,
	)
	if err != nil {
		log.Printf("slack: ingest error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

func (p *Plugin) sleep(ctx context.Context, seconds int) {
	timer := time.NewTimer(time.Duration(seconds) * time.Second)
	defer timer.Stop()
	select {
	case <-ctx.Done():
	case <-timer.C:
	}
}

func (p *Plugin) watchShutdown(ctx context.Context) {
	sc := make(chan os.Signal, 1)
	signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM)
	select {
	case <-ctx.Done():
	case <-sc:
	}
	_ = p.Stop()
}
