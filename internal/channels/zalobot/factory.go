package zalobot

import (
	"encoding/json"
	"fmt"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/channels"
	"github.com/nextlevelbuilder/goclaw/internal/config"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

// botCreds maps the encrypted credentials of a channel_instances row.
type botCreds struct {
	Token         string `json:"token"`
	WebhookSecret string `json:"webhook_secret,omitempty"`
}

// instanceConfig maps the non-secret config of a channel_instances row.
type instanceConfig struct {
	ConnectionMode string                     `json:"connection_mode,omitempty"`
	WebhookURL     string                     `json:"webhook_url,omitempty"`
	DMPolicy       string                     `json:"dm_policy,omitempty"`
	GroupPolicy    string                     `json:"group_policy,omitempty"`
	RequireMention *bool                      `json:"require_mention,omitempty"`
	MentionNames   []string                   `json:"mention_names,omitempty"`
	HistoryLimit   *int                       `json:"history_limit,omitempty"`
	MediaMaxMB     int                        `json:"media_max_mb,omitempty"`
	AllowFrom      []string                   `json:"allow_from,omitempty"`
	BlockReply     *bool                      `json:"block_reply,omitempty"`
	ChatBehavior   *config.ChatBehaviorConfig `json:"chat_behavior,omitempty"`
}

// Factory builds channels from DB instances. pendingStore persists group history
// across restarts; publicURL supplies the base URL for outbound local photos.
func Factory(pendingStore store.PendingMessageStore, publicURL func() string) channels.ChannelFactory {
	return func(name string, creds json.RawMessage, cfg json.RawMessage,
		msgBus *bus.MessageBus, pairingSvc store.PairingStore) (channels.Channel, error) {

		var c botCreds
		if len(creds) > 0 {
			if err := json.Unmarshal(creds, &c); err != nil {
				return nil, fmt.Errorf("decode zalo bot credentials: %w", err)
			}
		}
		var ic instanceConfig
		if len(cfg) > 0 {
			if err := json.Unmarshal(cfg, &ic); err != nil {
				return nil, fmt.Errorf("decode zalo bot config: %w", err)
			}
		}

		historyLimit := channels.DefaultGroupHistoryLimit
		if ic.HistoryLimit != nil {
			historyLimit = *ic.HistoryLimit
		}
		ch, err := New(Config{
			Token:          c.Token,
			WebhookSecret:  c.WebhookSecret,
			ConnectionMode: ic.ConnectionMode,
			WebhookURL:     ic.WebhookURL,
			DMPolicy:       ic.DMPolicy,
			GroupPolicy:    ic.GroupPolicy,
			RequireMention: ic.RequireMention != nil && *ic.RequireMention,
			MentionNames:   ic.MentionNames,
			HistoryLimit:   historyLimit,
			MediaMaxMB:     ic.MediaMaxMB,
			AllowFrom:      ic.AllowFrom,
			BlockReply:     ic.BlockReply,
			ChatBehavior:   ic.ChatBehavior,
			PublicURL:      publicURL,
		}, msgBus, pairingSvc, pendingStore)
		if err != nil {
			return nil, err
		}
		ch.SetName(name)
		return ch, nil
	}
}
