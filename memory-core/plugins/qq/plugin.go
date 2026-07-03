// Package qq implements the QQ source plugin via the OneBot protocol.
//
// It runs an HTTP webhook server compatible with OneBot 11/12 event reporting.
// Incoming JSON messages (group and private chat) are parsed and ingested
// into Memory Core.
//
// Supported OneBot versions: v11, v12.
//
// Security notes:
//   - The optional QQ_ACCESS_TOKEN acts as a shared secret between the
//     OneBot client and this plugin. If set, every incoming request must
//     carry a matching Authorization: Bearer <token> header; otherwise
//     HTTP 401 is returned.
//   - Messages sent by the bot itself (self_id) are filtered to prevent
//     event loops.
//   - The QQ_ACCESS_TOKEN must be kept secret; never log it.
package qq

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

// Plugin implements the SourcePlugin interface for QQ (OneBot).
type Plugin struct {
	accessToken string
	engine      *event.Engine
	addr        string // listen address, e.g. ":8081"
	server      *http.Server
	cancelFunc  context.CancelFunc
	selfID      string // the bot's own QQ number, used to filter self-messages
}

// New creates a new QQ (OneBot) plugin.
// The first argument is the event engine used to ingest messages.
//
// Environment variables:
//
//	QQ_ACCESS_TOKEN – optional shared secret for authenticating OneBot requests.
//	QQ_ADDR         – optional HTTP listen address (default ":8081").
//	QQ_SELF_ID      – optional bot's own QQ number for self-message filtering.
func New(engine *event.Engine) *Plugin {
	addr := os.Getenv("QQ_ADDR")
	if addr == "" {
		addr = ":8081"
	}
	return &Plugin{
		accessToken: os.Getenv("QQ_ACCESS_TOKEN"),
		engine:      engine,
		addr:        addr,
		selfID:      os.Getenv("QQ_SELF_ID"),
	}
}

// WithAccessToken sets the access token explicitly.
func (p *Plugin) WithAccessToken(token string) *Plugin {
	p.accessToken = token
	return p
}

// WithAddr sets the HTTP listen address.
func (p *Plugin) WithAddr(addr string) *Plugin {
	p.addr = addr
	return p
}

// WithSelfID sets the bot's own QQ number.
func (p *Plugin) WithSelfID(id string) *Plugin {
	p.selfID = id
	return p
}

// Name returns the plugin identifier.
func (p *Plugin) Name() string { return "qq" }

// Start begins the HTTP webhook server.
func (p *Plugin) Start(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/onebot", p.handleWebhook)
	// OneBot v11 uses / as the default path; support both.
	mux.HandleFunc("/", p.handleWebhook)

	p.server = &http.Server{
		Addr:    p.addr,
		Handler: mux,
	}

	ctx, p.cancelFunc = context.WithCancel(ctx)

	go func() {
		log.Printf("qq plugin: listening on %s", p.addr)
		if err := p.server.ListenAndServe(); err != http.ErrServerClosed {
			log.Printf("qq: server error: %v", err)
		}
	}()

	go p.watchShutdown(ctx)

	return nil
}

// Stop gracefully shuts down the HTTP server.
func (p *Plugin) Stop() error {
	if p.cancelFunc != nil {
		p.cancelFunc()
	}
	if p.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return p.server.Shutdown(ctx)
	}
	return nil
}

// Health checks whether the server is running.
func (p *Plugin) Health(ctx context.Context) error {
	if p.server == nil {
		return fmt.Errorf("qq: server not started")
	}
	return nil
}

// ---------------------------------------------------------------------------
// HTTP handler
// ---------------------------------------------------------------------------

func (p *Plugin) handleWebhook(w http.ResponseWriter, r *http.Request) {
	// Security: verify access token if configured.
	if p.accessToken != "" {
		auth := r.Header.Get("Authorization")
		expected := "Bearer " + p.accessToken
		if auth != expected {
			log.Printf("qq: access token mismatch from %s", r.RemoteAddr)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	}

	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.Printf("qq: read body error: %v", err)
		http.Error(w, "read error", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	// OneBot reports may batch events or send a single event.
	// Try to parse as a batch first, then as a single event.
	p.processBody(r.Context(), body)

	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_, _ = w.Write([]byte(`{"status":"ok"}`))
}

// ---------------------------------------------------------------------------
// event parsing (OneBot v11 / v12 compatible)
// ---------------------------------------------------------------------------

func (p *Plugin) processBody(ctx context.Context, raw []byte) {
	// OneBot v11: single object or array of objects.
	// OneBot v12: single object with "type" field.

	// Try array first (batch).
	var batch []json.RawMessage
	if json.Unmarshal(raw, &batch) == nil {
		for _, item := range batch {
			p.dispatchOneBotEvent(ctx, item)
		}
		return
	}

	// Single event.
	p.dispatchOneBotEvent(ctx, raw)
}

func (p *Plugin) dispatchOneBotEvent(ctx context.Context, raw json.RawMessage) {
	var envelope onebotEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		log.Printf("qq: unmarshal envelope error: %v", err)
		return
	}

	// OneBot v12 wraps the real event in "data"; try to unwrap.
	var evt onebotMessageEvent
	if envelope.Data != nil {
		_ = json.Unmarshal(envelope.Data, &evt)
	} else {
		_ = json.Unmarshal(raw, &evt)
	}

	// If post_type is missing, it's not an event we handle.
	if evt.PostType == "" {
		return
	}

	switch evt.PostType {
	case "message":
		p.handleOneBotMessage(ctx, &evt)
	case "message_sent":
		// OneBot v12 sends message_sent for the bot's own messages.
		// We ignore these — they've already been handled by the bot.
		return
	}
}

func (p *Plugin) handleOneBotMessage(ctx context.Context, evt *onebotMessageEvent) {
	// Only process text messages; ignore images, voice, etc.
	if evt.MessageType == "" && evt.RawMessage == "" {
		return
	}

	raw := evt.RawMessage
	if raw == "" {
		// Reconstruct text from message array (OneBot v11 "message" format).
		raw = p.reconstructText(evt.Message)
	}
	if raw == "" {
		return
	}

	// Security: filter self-messages to prevent event loops.
	selfIDStr := strconv.FormatInt(evt.SelfID, 10)
	if p.selfID != "" && p.selfID == selfIDStr {
		return
	}
	// Fallback for OneBot v11: user_id == self_id means it's a self-message.
	userIDStr := strconv.FormatInt(evt.UserID, 10)
	if userIDStr == selfIDStr {
		return
	}

	sessionID := ""
	switch evt.MessageType {
	case "private":
		sessionID = "private_" + userIDStr
	case "group":
		sessionID = "group_" + strconv.FormatInt(evt.GroupID, 10)
	default:
		sessionID = evt.MessageType + "_" + userIDStr
	}

	_, err := plugins.IngestToEvent(
		ctx,
		p.engine,
		userIDStr, // user_id
		"",        // agent_id
		sessionID, // session_id
		"qq",
		raw,
	)
	if err != nil {
		log.Printf("qq: ingest error: %v", err)
	}
}

// reconstructText builds a plain-text string from a OneBot v11 message array.
// Each segment of type "text" has its data.text concatenated.
func (p *Plugin) reconstructText(segments json.RawMessage) string {
	if len(segments) == 0 {
		return ""
	}
	var arr []onebotMessageSegment
	if err := json.Unmarshal(segments, &arr); err != nil {
		return ""
	}
	var b strings.Builder
	for _, seg := range arr {
		if seg.Type == "text" {
			b.WriteString(seg.Data.Text)
		}
	}
	return b.String()
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
// OneBot JSON types
// ---------------------------------------------------------------------------

// onebotEnvelope is the top-level OneBot v12 event envelope.
type onebotEnvelope struct {
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

// onebotMessageEvent covers both OneBot v11 and v12 message events.
type onebotMessageEvent struct {
	PostType    string          `json:"post_type"`    // "message" or "message_sent"
	MessageType string          `json:"message_type"` // "private" or "group"
	UserID      int64           `json:"user_id"`
	GroupID     int64           `json:"group_id"`
	SelfID      int64           `json:"self_id"`
	RawMessage  string          `json:"raw_message"` // v11
	Message     json.RawMessage `json:"message"`     // v11 message array
}

// onebotMessageSegment is a single segment in a OneBot v11 message array.
type onebotMessageSegment struct {
	Type string            `json:"type"`
	Data onebotSegmentData `json:"data"`
}

type onebotSegmentData struct {
	Text string `json:"text"`
}
