package zalobot

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/nextlevelbuilder/goclaw/internal/channels"
	"github.com/nextlevelbuilder/goclaw/internal/channels/media"
	"github.com/nextlevelbuilder/goclaw/internal/channels/typing"
	"github.com/nextlevelbuilder/goclaw/internal/store"
)

const pairingDebounce = 60 * time.Second

// processUpdate applies access policy and forwards one event to the agent.
func (c *Channel) processUpdate(u update) {
	if !c.IsRunning() {
		return
	}
	switch u.EventName {
	case eventText, eventImage, eventSticker, eventVoice:
	case eventUnsupported:
		slog.Debug("zalo_bot: unsupported message type ignored", "channel", c.Name(), "message_id", u.Message.MessageID)
		return
	default:
		slog.Debug("zalo_bot: unknown event ignored", "channel", c.Name(), "event", u.EventName)
		return
	}

	msg := &u.Message
	senderID := msg.From.ID
	if senderID == "" || msg.From.IsBot {
		return
	}
	chatID := msg.Chat.ID
	if chatID == "" {
		chatID = senderID
	}
	if !c.seen.markNew(msg.MessageID) {
		return
	}

	ctx := store.WithTenantID(context.Background(), c.TenantID())
	if msg.isGroup() {
		c.groupChats.Store(chatID, true)
		if c.allowGroup(ctx, senderID, chatID) {
			c.handleGroup(ctx, u.EventName, msg, chatID)
		}
		return
	}
	if c.allowDM(ctx, senderID, chatID) {
		c.handleDM(ctx, u.EventName, msg, chatID)
	}
}

func (c *Channel) allowDM(ctx context.Context, senderID, chatID string) bool {
	switch c.CheckDMPolicy(ctx, senderID, c.cfg.DMPolicy) {
	case channels.PolicyAllow:
		return true
	case channels.PolicyNeedsPairing:
		c.sendPairingReply(ctx, senderID, chatID)
	default:
		slog.Debug("zalo_bot: DM rejected by policy", "channel", c.Name(), "sender_id", senderID)
	}
	return false
}

func (c *Channel) allowGroup(ctx context.Context, senderID, chatID string) bool {
	switch c.CheckGroupPolicy(ctx, senderID, chatID, c.cfg.GroupPolicy) {
	case channels.PolicyAllow:
		return true
	case channels.PolicyNeedsPairing:
		c.sendPairingReply(ctx, "group:"+chatID, chatID)
	default:
		slog.Debug("zalo_bot: group message rejected by policy", "channel", c.Name(), "group_id", chatID)
	}
	return false
}

func (c *Channel) sendPairingReply(ctx context.Context, senderID, chatID string) {
	ps := c.PairingService()
	if ps == nil || !c.CanSendPairingNotif(senderID, pairingDebounce) {
		return
	}
	code, err := ps.RequestPairing(ctx, senderID, c.Name(), chatID, "default", nil)
	if err != nil {
		slog.Debug("zalo_bot: pairing request failed", "channel", c.Name(), "error", err)
		return
	}
	reply := fmt.Sprintf(
		"GoClaw: access not configured.\n\nYour Zalo user id: %s\n\nPairing code: %s\n\nAsk the bot owner to approve with:\n  goclaw pairing approve %s",
		senderID, code, code,
	)
	if err := c.api.sendMessage(ctx, chatID, reply, ""); err != nil {
		slog.Warn("zalo_bot: failed to send pairing reply", "channel", c.Name(), "error", err)
		return
	}
	c.MarkPairingNotifSent(senderID)
}

func (c *Channel) handleDM(ctx context.Context, event string, msg *message, chatID string) {
	content, mediaPaths := c.extractContent(ctx, event, msg)
	if content == "" {
		return
	}
	c.collectContact(ctx, msg, "direct")
	c.startTyping(chatID)
	c.HandleMessage(msg.From.ID, chatID, content, mediaPaths, c.metadata(msg), "direct")
}

func (c *Channel) handleGroup(ctx context.Context, event string, msg *message, chatID string) {
	content, mediaPaths := c.extractContent(ctx, event, msg)
	if content == "" {
		return
	}
	senderName := senderLabel(msg)
	c.collectContact(ctx, msg, "group")

	if c.RequireMention() && !c.mentionsBot(msg.Text+" "+msg.Caption) {
		slog.Info("zalo_bot: group message without bot mention kept as history",
			"channel", c.Name(), "group_id", chatID, "message_id", msg.MessageID)
		c.GroupHistory().Record(chatID, channels.HistoryEntry{
			Sender:    senderName,
			SenderID:  msg.From.ID,
			Body:      content,
			Media:     mediaPaths,
			Timestamp: time.Now(),
			MessageID: msg.MessageID,
		}, c.HistoryLimit())
		return
	}

	finalContent := fmt.Sprintf("[From: %s]\n%s", senderName, content)
	if c.HistoryLimit() > 0 {
		finalContent = c.GroupHistory().BuildContext(chatID, finalContent, c.HistoryLimit())
	}
	// Must follow BuildContext: CollectMedia clears the entries' media fields.
	allMedia := append(c.GroupHistory().CollectMedia(chatID), mediaPaths...)

	c.startTyping(chatID)
	metadata := c.metadata(msg)
	metadata["group_id"] = chatID
	c.HandleMessage(msg.From.ID, chatID, finalContent, allMedia, metadata, "group")
	c.GroupHistory().Clear(chatID)
}

// mentionsBot looks for "@<name>" in the text, using the account name from getMe
// and the configured mention names. The API has no mention field, so the
// display form is the only signal available.
func (c *Channel) mentionsBot(text string) bool {
	lower := strings.ToLower(text)
	for _, name := range append([]string{c.botName}, c.cfg.MentionNames...) {
		name = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(name), "@"))
		if name != "" && strings.Contains(lower, "@"+strings.ToLower(name)) {
			return true
		}
	}
	return false
}

// extractContent builds the agent-facing text and downloads any attachment.
func (c *Channel) extractContent(ctx context.Context, event string, msg *message) (string, []string) {
	switch event {
	case eventText:
		return msg.Text, nil
	case eventImage:
		return c.withAttachment(ctx, msg.PhotoURL, media.TypeImage, firstNonEmpty(msg.Caption, msg.Text), "[image]")
	case eventSticker:
		return c.withAttachment(ctx, msg.URL, media.TypeImage, "", "[sticker]")
	case eventVoice:
		return c.withAttachment(ctx, msg.VoiceURL, media.TypeVoice, "", "[voice message]")
	}
	return "", nil
}

func (c *Channel) withAttachment(ctx context.Context, fileURL, kind, text, placeholder string) (string, []string) {
	if text == "" {
		text = placeholder
	}
	if fileURL == "" {
		return text, nil
	}
	path, err := downloadFile(ctx, fileURL, int64(c.cfg.MediaMaxMB)<<20)
	if err != nil {
		slog.Warn("zalo_bot: media download failed", "channel", c.Name(), "kind", kind, "error", err)
		return text, nil
	}
	tag := media.BuildMediaTags([]media.MediaInfo{{Type: kind, FilePath: path, ContentType: media.DetectMIMEType(path)}})
	return tag + "\n" + text, []string{path}
}

func (c *Channel) metadata(msg *message) map[string]string {
	return map[string]string{
		"message_id":   msg.MessageID,
		"platform":     channels.TypeZaloBot,
		"display_name": channels.SanitizeDisplayName(senderLabel(msg)),
	}
}

func (c *Channel) collectContact(ctx context.Context, msg *message, peerKind string) {
	if cc := c.ContactCollector(); cc != nil {
		cc.EnsureContact(ctx, c.Type(), c.Name(), msg.From.ID, msg.From.ID, senderLabel(msg), "", peerKind, "user", "", "")
	}
}

func (c *Channel) startTyping(chatID string) {
	ctrl := typing.New(typing.Options{
		MaxDuration:       60 * time.Second,
		KeepaliveInterval: 4 * time.Second,
		StartFn: func() error {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			return c.api.sendTyping(ctx, chatID)
		},
	})
	if prev, ok := c.typingCtrls.Swap(chatID, ctrl); ok {
		if old, ok := prev.(*typing.Controller); ok {
			old.Stop()
		}
	}
	ctrl.Start()
}

func senderLabel(msg *message) string {
	return firstNonEmpty(msg.From.DisplayName, msg.From.ID)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
