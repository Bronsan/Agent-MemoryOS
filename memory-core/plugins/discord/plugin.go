// Package discord implements the Discord source plugin.
// It connects to Discord via bot token, listens for messages, and ingests
// them as raw events into the Memory Core event stream.
package discord

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/agent-memoryos/memory-core/event"
	"github.com/agent-memoryos/memory-core/plugins"
	"github.com/bwmarrin/discordgo"
)

// Plugin implements the SourcePlugin interface for Discord.
type Plugin struct {
	token      string
	engine     *event.Engine
	session    *discordgo.Session
	cancelFunc context.CancelFunc
}

// New creates a new Discord plugin.
func New(engine *event.Engine) *Plugin {
	return &Plugin{
		token:  os.Getenv("DISCORD_BOT_TOKEN"),
		engine: engine,
	}
}

// WithToken sets the bot token explicitly (overrides env var).
func (p *Plugin) WithToken(token string) *Plugin {
	p.token = token
	return p
}

// Name returns the plugin identifier.
func (p *Plugin) Name() string { return "discord" }

// Start connects to Discord and begins listening for messages.
func (p *Plugin) Start(ctx context.Context) error {
	if p.token == "" {
		return fmt.Errorf("discord: DISCORD_BOT_TOKEN not set")
	}

	session, err := discordgo.New("Bot " + p.token)
	if err != nil {
		return fmt.Errorf("discord: create session: %w", err)
	}

	session.Identify.Intents = discordgo.IntentsGuildMessages | discordgo.IntentsDirectMessages

	// Register message handler
	session.AddHandler(func(s *discordgo.Session, m *discordgo.MessageCreate) {
		p.handleMessage(ctx, s, m)
	})

	if err := session.Open(); err != nil {
		return fmt.Errorf("discord: open connection: %w", err)
	}

	p.session = session
	ctx, p.cancelFunc = context.WithCancel(ctx)

	go p.watchShutdown(ctx, session)

	log.Println("discord plugin: connected")
	return nil
}

// Stop disconnects from Discord.
func (p *Plugin) Stop() error {
	if p.cancelFunc != nil {
		p.cancelFunc()
	}
	if p.session != nil {
		return p.session.Close()
	}
	return nil
}

// Health checks if the Discord connection is alive.
func (p *Plugin) Health(ctx context.Context) error {
	if p.session == nil {
		return fmt.Errorf("discord: not connected")
	}
	if p.session.DataReady {
		return nil
	}
	return fmt.Errorf("discord: session not ready")
}

func (p *Plugin) handleMessage(ctx context.Context, s *discordgo.Session, m *discordgo.MessageCreate) {
	// Ignore bot messages to prevent loops
	if m.Author.Bot {
		return
	}

	// Ignore empty messages
	if m.Content == "" {
		return
	}

	// Ingest the message as a raw event
	_, err := plugins.IngestToEvent(
		ctx,
		p.engine,
		m.Author.ID,
		"",          // agent_id
		m.ChannelID, // session_id = channel
		"discord",
		m.Content,
	)

	if err != nil {
		log.Printf("discord: ingest error: %v", err)
	}
}

func (p *Plugin) watchShutdown(ctx context.Context, session *discordgo.Session) {
	sc := make(chan os.Signal, 1)
	signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM)

	select {
	case <-ctx.Done():
	case <-sc:
	}
	_ = session.Close()
}
