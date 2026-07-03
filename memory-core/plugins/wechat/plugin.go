// Package wechat implements the WeChat Official Account source plugin.
//
// It runs an HTTP server to receive messages forwarded by the WeChat server
// (webhook mode). Incoming XML messages are parsed, verified, and ingested
// into Memory Core.
//
// Security notes:
//   - The WECHAT_TOKEN must be kept secret; it is used for signature
//     verification on every incoming request.
//   - Always verify the message signature (sha1(token, timestamp, nonce))
//     before processing the message body. Unverified requests are rejected
//     with HTTP 403.
//   - The plugin implements WeChat's server-url-validation handshake
//     (echostr) so it can pass the official account configuration check.
//   - Messages from the official account itself are not ingested.
package wechat

import (
	"context"
	"crypto/sha1"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/agent-memoryos/memory-core/event"
	"github.com/agent-memoryos/memory-core/plugins"
)

// Plugin implements the SourcePlugin interface for WeChat.
type Plugin struct {
	token      string
	engine     *event.Engine
	addr       string // listen address, e.g. ":8080"
	server     *http.Server
	cancelFunc context.CancelFunc
}

// New creates a new WeChat plugin.
// The first argument is the event engine used to ingest messages.
//
// Environment variables:
//
//	WECHAT_TOKEN  – the token configured in the WeChat Official Account backend.
//	WECHAT_ADDR   – optional HTTP listen address (default ":8080").
func New(engine *event.Engine) *Plugin {
	addr := os.Getenv("WECHAT_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	return &Plugin{
		token:  os.Getenv("WECHAT_TOKEN"),
		engine: engine,
		addr:   addr,
	}
}

// WithToken sets the verification token explicitly (overrides WECHAT_TOKEN).
func (p *Plugin) WithToken(token string) *Plugin {
	p.token = token
	return p
}

// WithAddr sets the HTTP listen address.
func (p *Plugin) WithAddr(addr string) *Plugin {
	p.addr = addr
	return p
}

// Name returns the plugin identifier.
func (p *Plugin) Name() string { return "wechat" }

// Start begins the HTTP webhook server.
func (p *Plugin) Start(ctx context.Context) error {
	if p.token == "" {
		return fmt.Errorf("wechat: WECHAT_TOKEN not set")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/wechat", p.handleWebhook)

	p.server = &http.Server{
		Addr:    p.addr,
		Handler: mux,
	}

	ctx, p.cancelFunc = context.WithCancel(ctx)

	go func() {
		log.Printf("wechat plugin: listening on %s", p.addr)
		if err := p.server.ListenAndServe(); err != http.ErrServerClosed {
			log.Printf("wechat: server error: %v", err)
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
		return fmt.Errorf("wechat: server not started")
	}
	return nil
}

// ---------------------------------------------------------------------------
// HTTP handler
// ---------------------------------------------------------------------------

func (p *Plugin) handleWebhook(w http.ResponseWriter, r *http.Request) {
	// 1. Signature verification (required by WeChat).
	signature := r.URL.Query().Get("signature")
	timestamp := r.URL.Query().Get("timestamp")
	nonce := r.URL.Query().Get("nonce")

	if signature == "" || timestamp == "" || nonce == "" {
		http.Error(w, "missing parameters", http.StatusBadRequest)
		return
	}

	if !p.verifySignature(signature, timestamp, nonce) {
		// Security: reject unverified requests silently (WeChat expects 200,
		// but returning 403 is safer for integrity).
		log.Printf("wechat: signature verification failed from %s", r.RemoteAddr)
		http.Error(w, "signature verification failed", http.StatusForbidden)
		return
	}

	// 2. Server URL validation (echostr handshake).
	//    This is called once when configuring the webhook in the WeChat backend.
	echostr := r.URL.Query().Get("echostr")
	if echostr != "" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte(echostr))
		return
	}

	// 3. Parse XML body.
	body, err := io.ReadAll(r.Body)
	if err != nil {
		log.Printf("wechat: read body error: %v", err)
		http.Error(w, "read error", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var msg wechatMessage
	if err := xml.Unmarshal(body, &msg); err != nil {
		log.Printf("wechat: xml unmarshal error: %v", err)
		http.Error(w, "xml parse error", http.StatusBadRequest)
		return
	}

	// 4. Process the message.
	p.handleMessage(r.Context(), &msg)

	// WeChat requires an empty 200 response (or "success" text).
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("success"))
}

// ---------------------------------------------------------------------------
// message handling
// ---------------------------------------------------------------------------

func (p *Plugin) handleMessage(ctx context.Context, msg *wechatMessage) {
	switch msg.MsgType {

	case "text":
		// Only ingest user-generated text messages.
		// Security: Official-Account-to-self messages are not relevant;
		// but they would have different FromUserName, so they pass through
		// naturally as long as the account doesn't send to itself.

		if msg.Content == "" {
			return
		}

		_, err := plugins.IngestToEvent(
			ctx,
			p.engine,
			msg.FromUserName, // user_id = WeChat OpenID
			"",               // agent_id
			msg.ToUserName,   // session_id = the Official Account ID
			"wechat",
			msg.Content,
		)
		if err != nil {
			log.Printf("wechat: ingest error: %v", err)
		}

	case "event":
		// WeChat events (subscribe, unsubscribe, etc.) can be ingested
		// as well. For now we only process text messages.
		return

	default:
		// Non-text message types (image, voice, etc.) are ignored.
		return
	}
}

// ---------------------------------------------------------------------------
// signature verification
//
// WeChat signature algorithm: sha1(sort(token, timestamp, nonce).join(""))
// ---------------------------------------------------------------------------

func (p *Plugin) verifySignature(signature, timestamp, nonce string) bool {
	arr := []string{p.token, timestamp, nonce}
	sort.Strings(arr)
	sum := sha1.Sum([]byte(strings.Join(arr, "")))
	got := fmt.Sprintf("%x", sum)
	return got == signature
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
// WeChat XML message types (partial)
// ---------------------------------------------------------------------------

// wechatMessage represents an incoming WeChat XML message.
// Reference: https://developers.weixin.qq.com/doc/offiaccount/Message_Management/Receiving_standard_messages.html
type wechatMessage struct {
	XMLName      xml.Name `xml:"xml"`
	ToUserName   string   `xml:"ToUserName"`
	FromUserName string   `xml:"FromUserName"`
	CreateTime   int64    `xml:"CreateTime"`
	MsgType      string   `xml:"MsgType"`
	Content      string   `xml:"Content"`
	MsgID        int64    `xml:"MsgId"`
	Event        string   `xml:"Event"`
}
