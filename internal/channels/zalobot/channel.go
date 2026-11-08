// Package zalobot implements the Zalo Bot Platform channel.
//
// API docs: https://docs.zaloplatforms.com/docs/BOT
// Bots receive events by long polling or by webhook (never both), can join
// groups, and deliver photos only by public URL.
package zalobot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/channels"
	"github.com/nextlevelbuilder/goclaw/internal/channels/typing"
	"github.com/nextlevelbuilder/goclaw/internal/config"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

const (
	modePolling = "polling"
	modeWebhook = "webhook"

	defaultMediaMaxMB = 20
	minSecretLength   = 8
	maxSecretLength   = 256

	pollTimeoutSeconds  = 30
	pollTimeoutHeadroom = 7 * time.Second
	pollErrorBackoff    = 5 * time.Second
	pollStopWait        = 5 * time.Second

	webhookVerifyAttempts = 5
	webhookVerifyBackoff  = 15 * time.Second
)

// Config is the resolved configuration of one Zalo Bot instance.
type Config struct {
	Token          string
	WebhookSecret  string
	ConnectionMode string
	WebhookURL     string
	DMPolicy       string
	GroupPolicy    string
	RequireMention bool
	// MentionNames are extra names that count as a mention, such as the display
	// name members see in Zalo. getMe only returns the account name.
	MentionNames []string
	HistoryLimit int
	MediaMaxMB   int
	AllowFrom    []string
	BlockReply   *bool
	ChatBehavior *config.ChatBehaviorConfig
	// PublicURL returns the gateway's public base URL. Zalo can only fetch
	// outbound photos by URL, so local files need a reachable address.
	PublicURL func() string
}

// Channel connects one Zalo bot to the gateway.
type Channel struct {
	*channels.BaseChannel
	cfg     Config
	api     *apiClient
	botName string

	seen        seenMessages
	groupChats  sync.Map // chatID → true, for chats seen with chat_type GROUP
	typingCtrls sync.Map // chatID → *typing.Controller

	cancel   context.CancelFunc
	pollDone chan struct{} // closed when pollLoop exits; nil in webhook mode
	stopOnce sync.Once
}

// New validates cfg and builds a channel. It makes no network calls.
func New(cfg Config, msgBus *bus.MessageBus, pairingSvc store.PairingStore, pendingStore store.PendingMessageStore) (*Channel, error) {
	// Pasted credentials often carry a trailing newline, which would make every
	// Bot API URL invalid because the token is part of the path.
	cfg.Token = strings.TrimSpace(cfg.Token)
	cfg.WebhookSecret = strings.TrimSpace(cfg.WebhookSecret)
	if cfg.Token == "" {
		return nil, errors.New("zalo bot token is required")
	}
	if cfg.ConnectionMode == "" {
		cfg.ConnectionMode = modePolling
	}
	switch cfg.ConnectionMode {
	case modePolling:
	case modeWebhook:
		if err := validateWebhookConfig(cfg.WebhookURL, cfg.WebhookSecret); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("zalo bot connection_mode must be %q or %q", modePolling, modeWebhook)
	}
	if cfg.DMPolicy == "" {
		cfg.DMPolicy = "pairing"
	}
	if cfg.GroupPolicy == "" {
		cfg.GroupPolicy = "pairing"
	}
	if cfg.MediaMaxMB <= 0 {
		cfg.MediaMaxMB = defaultMediaMaxMB
	}

	base := channels.NewBaseChannel(channels.TypeZaloBot, msgBus, cfg.AllowFrom)
	base.ValidatePolicy(cfg.DMPolicy, cfg.GroupPolicy)

	ch := &Channel{BaseChannel: base, cfg: cfg, api: newAPIClient(cfg.Token)}
	ch.SetPairingService(pairingSvc)
	ch.SetGroupHistory(channels.MakeHistory(channels.TypeZaloBot, pendingStore, base.TenantID()))
	ch.SetHistoryLimit(cfg.HistoryLimit)
	ch.SetRequireMention(cfg.RequireMention)
	return ch, nil
}

func validateWebhookConfig(webhookURL, secret string) error {
	parsed, err := url.Parse(webhookURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return errors.New("zalo bot webhook mode requires a public https webhook_url")
	}
	if len(secret) < minSecretLength || len(secret) > maxSecretLength {
		return fmt.Errorf("zalo bot webhook_secret must be %d-%d characters", minSecretLength, maxSecretLength)
	}
	return nil
}

// BlockReplyEnabled returns the per-channel block_reply override (nil = inherit gateway default).
func (c *Channel) BlockReplyEnabled() *bool { return c.cfg.BlockReply }

// ChatBehaviorConfig returns the per-channel chat_behavior override.
func (c *Channel) ChatBehaviorConfig() *config.ChatBehaviorConfig { return c.cfg.ChatBehavior }

// SetPendingCompaction configures auto-compaction for group history.
func (c *Channel) SetPendingCompaction(cfg *channels.CompactionConfig) {
	if gh := c.GroupHistory(); gh != nil {
		gh.SetCompactionConfig(cfg)
	}
}

// SetPendingHistoryTenantID propagates tenant_id to the pending history for DB operations.
func (c *Channel) SetPendingHistoryTenantID(id uuid.UUID) {
	if gh := c.GroupHistory(); gh != nil {
		gh.SetTenantID(id)
	}
}

// Start validates the token and begins receiving events. The caller's ctx may be
// a short startup deadline, so background work runs on its own context.
func (c *Channel) Start(ctx context.Context) error {
	c.MarkStarting("Connecting to Zalo Bot")
	info, err := c.api.getMe(ctx)
	if err != nil {
		c.MarkFailed("Zalo Bot token check failed", err.Error(), classifyFailure(err), true)
		return err
	}
	c.botName = info.AccountName

	runCtx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	c.SetRunning(true)

	if c.cfg.ConnectionMode == modeWebhook {
		if err := c.startWebhook(ctx, runCtx); err != nil {
			cancel()
			c.SetRunning(false)
			c.MarkFailed("Zalo Bot webhook setup failed", err.Error(), classifyFailure(err), true)
			return err
		}
	} else {
		// getUpdates fails while a webhook is registered, so clear any leftover.
		if err := c.api.deleteWebhook(ctx); err != nil {
			slog.Warn("zalo_bot: deleteWebhook failed before polling", "channel", c.Name(), "error", err)
		}
		c.MarkHealthy("Connected (polling)")
		c.pollDone = make(chan struct{})
		go func() {
			defer close(c.pollDone)
			c.pollLoop(runCtx)
		}()
	}
	slog.Info("zalo_bot connected", "channel", c.Name(), "bot_id", info.ID, "bot_name", info.AccountName, "mode", c.cfg.ConnectionMode)
	return nil
}

// Stop halts event intake. A registered webhook stays in place so a restart does
// not lose events; switching to polling clears it on the next Start.
func (c *Channel) Stop(ctx context.Context) error {
	c.stopOnce.Do(func() {
		if c.cancel != nil {
			c.cancel()
		}
		// A reload starts the replacement right after Stop, and a poller still in
		// flight would consume events meant for it.
		if c.pollDone != nil {
			select {
			case <-c.pollDone:
			case <-ctx.Done():
			case <-time.After(pollStopWait):
				slog.Warn("zalo_bot: poll loop still running after stop", "channel", c.Name())
			}
		}
		router.unregister(c)
		c.typingCtrls.Range(func(key, val any) bool {
			if ctrl, ok := val.(*typing.Controller); ok {
				ctrl.Stop()
			}
			c.typingCtrls.Delete(key)
			return true
		})
		c.SetRunning(false)
		c.MarkStopped("Stopped")
	})
	return nil
}

func (c *Channel) startWebhook(startCtx, runCtx context.Context) error {
	if err := router.register(c); err != nil {
		return err
	}
	verified, err := c.registerWebhook(startCtx)
	if err != nil {
		router.unregister(c)
		return err
	}
	if verified {
		c.MarkHealthy("Connected (webhook)")
		return nil
	}
	// During gateway boot the HTTP listener may not be up yet, so a failed probe
	// is expected once. Retry before leaving the channel degraded.
	go c.retryWebhookVerification(runCtx)
	return nil
}

// registerWebhook calls setWebhook and reports whether Zalo reached the URL.
func (c *Channel) registerWebhook(ctx context.Context) (bool, error) {
	result, err := c.api.setWebhook(ctx, c.cfg.WebhookURL, c.cfg.WebhookSecret)
	if err != nil {
		return false, err
	}
	v := result.Verification
	if v == nil || v.OK {
		return true, nil
	}
	c.MarkDegraded("Zalo cannot reach the webhook URL", v.Outcome+": "+v.Hint, channels.ChannelFailureKindNetwork, true)
	slog.Warn("zalo_bot: webhook verification failed", "channel", c.Name(), "outcome", v.Outcome)
	return false, nil
}

func (c *Channel) retryWebhookVerification(ctx context.Context) {
	for range webhookVerifyAttempts {
		select {
		case <-ctx.Done():
			return
		case <-time.After(webhookVerifyBackoff):
		}
		verified, err := c.registerWebhook(ctx)
		if err != nil {
			slog.Warn("zalo_bot: setWebhook retry failed", "channel", c.Name(), "error", err)
			continue
		}
		if verified {
			c.MarkHealthy("Connected (webhook)")
			return
		}
	}
}

func (c *Channel) pollLoop(ctx context.Context) {
	degraded := false
	for ctx.Err() == nil {
		pollCtx, cancel := context.WithTimeout(ctx, pollTimeoutSeconds*time.Second+pollTimeoutHeadroom)
		updates, err := c.api.getUpdates(pollCtx, pollTimeoutSeconds)
		cancel()

		if err != nil && !isNoUpdates(err) && !errors.Is(err, context.DeadlineExceeded) {
			if ctx.Err() != nil {
				return
			}
			slog.Warn("zalo_bot: getUpdates failed", "channel", c.Name(), "error", err)
			c.MarkDegraded("Polling Zalo Bot failed", err.Error(), classifyFailure(err), true)
			degraded = true
			select {
			case <-ctx.Done():
				return
			case <-time.After(pollErrorBackoff):
			}
			continue
		}
		if degraded {
			c.MarkHealthy("Connected (polling)")
			degraded = false
		}
		for _, u := range updates {
			c.processUpdate(u)
		}
	}
}

func classifyFailure(err error) channels.ChannelFailureKind {
	var ae *apiError
	if errors.As(err, &ae) {
		if ae.Code == 401 || ae.Code == 403 || strings.Contains(strings.ToLower(ae.Description), "token") {
			return channels.ChannelFailureKindAuth
		}
		return channels.ChannelFailureKindUnknown
	}
	return channels.ChannelFailureKindNetwork
}
