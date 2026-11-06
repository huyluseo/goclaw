package zalobot

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/channels"
	"github.com/nextlevelbuilder/goclaw/internal/channels/media"
	"github.com/nextlevelbuilder/goclaw/internal/channels/typing"
	"github.com/nextlevelbuilder/goclaw/internal/channels/zalo"
)

const (
	maxTextLength = 2000
	parseMarkdown = "markdown"
)

// Send delivers an agent reply. An empty message only clears the typing indicator.
func (c *Channel) Send(ctx context.Context, msg bus.OutboundMessage) error {
	if !c.IsRunning() {
		return errors.New("zalo_bot channel not running")
	}
	if ctrl, ok := c.typingCtrls.LoadAndDelete(msg.ChatID); ok {
		ctrl.(*typing.Controller).Stop()
	}

	var mediaErrs []error
	for _, attachment := range msg.Media {
		if err := c.sendAttachment(ctx, msg.ChatID, attachment); err != nil {
			slog.Warn("zalo_bot: failed to send attachment", "channel", c.Name(), "file", filepath.Base(attachment.URL), "error", err)
			mediaErrs = append(mediaErrs, err)
		}
	}
	if msg.Content != "" {
		mediaErrs = append(mediaErrs, c.sendText(ctx, msg.ChatID, msg.Content))
	}
	return errors.Join(mediaErrs...)
}

// sendText posts Markdown, falling back to plain text for a chunk Zalo rejects.
func (c *Channel) sendText(ctx context.Context, chatID, text string) error {
	for _, chunk := range channels.ChunkMarkdownBySentence(compactListSpacing(text), maxTextLength) {
		err := c.api.sendMessage(ctx, chatID, chunk, parseMarkdown)
		var ae *apiError
		if errors.As(err, &ae) {
			err = c.api.sendMessage(ctx, chatID, zalo.StripMarkdown(chunk), "")
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func (c *Channel) sendAttachment(ctx context.Context, chatID string, attachment bus.MediaAttachment) error {
	contentType := attachment.ContentType
	if contentType == "" {
		contentType = media.DetectMIMEType(attachment.URL)
	}
	switch {
	case strings.HasPrefix(contentType, "image/"):
		photoURL, err := c.reachableURL(attachment.URL)
		if err != nil {
			return err
		}
		return c.api.sendPhoto(ctx, chatID, photoURL, truncateRunes(attachment.Caption, maxTextLength))
	case isAACFile(attachment.URL):
		// The API documents sendVoice for 1-1 chats only.
		if _, isGroup := c.groupChats.Load(chatID); isGroup {
			return errors.New("voice messages are only delivered in 1-1 chats")
		}
		voiceURL, err := c.reachableURL(attachment.URL)
		if err != nil {
			return err
		}
		return c.api.sendVoice(ctx, chatID, voiceURL)
	}
	return fmt.Errorf("zalo bot cannot send %s attachments", contentType)
}

// reachableURL returns a URL Zalo can fetch, publishing local files through the
// gateway because the API has no file upload.
func (c *Channel) reachableURL(location string) (string, error) {
	if strings.HasPrefix(location, "https://") || strings.HasPrefix(location, "http://") {
		return location, nil
	}
	base := c.publicBaseURL()
	if base == "" {
		return "", errors.New("gateway public URL unknown: set webhook_url or open the dashboard through its public address")
	}
	token, err := router.publishMedia(location)
	if err != nil {
		return "", err
	}
	return base + mediaPathPrefix + token, nil
}

func (c *Channel) publicBaseURL() string {
	if parsed, err := url.Parse(c.cfg.WebhookURL); err == nil && parsed.Scheme != "" && parsed.Host != "" {
		return parsed.Scheme + "://" + parsed.Host
	}
	if c.cfg.PublicURL != nil {
		return strings.TrimRight(c.cfg.PublicURL(), "/")
	}
	return ""
}

func isAACFile(location string) bool {
	clean, _, _ := strings.Cut(location, "?")
	return strings.EqualFold(filepath.Ext(clean), ".aac")
}

func truncateRunes(s string, limit int) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit])
}
