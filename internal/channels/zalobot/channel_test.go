package zalobot

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/channels"
)

func TestNewValidatesConfig(t *testing.T) {
	const url = "https://example.com" + WebhookPath
	tests := []struct {
		name    string
		cfg     Config
		wantErr string
	}{
		{"missing token", Config{}, "token"},
		{"unknown mode", Config{Token: "t", ConnectionMode: "carrier-pigeon"}, "connection_mode"},
		{"webhook without url", Config{Token: "t", ConnectionMode: modeWebhook, WebhookSecret: "12345678"}, "https"},
		{"webhook over http", Config{Token: "t", ConnectionMode: modeWebhook, WebhookURL: "http://example.com/x", WebhookSecret: "12345678"}, "https"},
		{"short secret", Config{Token: "t", ConnectionMode: modeWebhook, WebhookURL: url, WebhookSecret: "short"}, "webhook_secret"},
		{"polling defaults", Config{Token: "t"}, ""},
		{"valid webhook", Config{Token: "t", ConnectionMode: modeWebhook, WebhookURL: url, WebhookSecret: "12345678"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := New(tt.cfg, bus.New(), nil, nil)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("got %v, want error containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestNewTrimsPastedCredentials(t *testing.T) {
	ch, err := New(Config{Token: " abc\r\n", WebhookSecret: "12345678\n"}, bus.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if ch.cfg.Token != "abc" || ch.cfg.WebhookSecret != "12345678" {
		t.Fatal("credentials were not trimmed")
	}
	if _, err := New(Config{Token: " \n"}, bus.New(), nil, nil); err == nil {
		t.Fatal("a blank token must be rejected")
	}
}

func TestFactoryReadsCredentialsAndConfig(t *testing.T) {
	creds := json.RawMessage(`{"token":"abc","webhook_secret":"12345678"}`)
	cfg := json.RawMessage(`{"connection_mode":"webhook","webhook_url":"https://example.com/hook","require_mention":true,"history_limit":7,"allow_from":["u1"]}`)

	built, err := Factory(nil, nil)("my-bot", creds, cfg, bus.New(), nil)
	if err != nil {
		t.Fatal(err)
	}
	ch := built.(*Channel)
	if ch.Name() != "my-bot" || ch.cfg.ConnectionMode != modeWebhook || !ch.RequireMention() || ch.HistoryLimit() != 7 {
		t.Fatalf("unexpected channel: name=%s cfg=%+v", ch.Name(), ch.cfg)
	}
}

func TestFactoryDefaultsHistoryLimit(t *testing.T) {
	built, err := Factory(nil, nil)("b", json.RawMessage(`{"token":"abc"}`), nil, bus.New(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := built.(*Channel).HistoryLimit(); got != channels.DefaultGroupHistoryLimit {
		t.Fatalf("history limit = %d", got)
	}
}

func TestPollingDeliversMessagesAndStops(t *testing.T) {
	fake := startFakeAPI(t)
	delivered := false
	fake.handlers["getUpdates"] = func(map[string]any) string {
		if delivered {
			return `{"ok":false,"error_code":408,"description":"no updates"}`
		}
		delivered = true
		return `{"ok":true,"result":{"event_name":"message.text.received","message":{"message_id":"m1","text":"hello","from":{"id":"u1","display_name":"An"},"chat":{"id":"c1","chat_type":"PRIVATE"}}}}`
	}

	msgBus := bus.New()
	ch, err := New(Config{Token: testToken, DMPolicy: "open"}, msgBus, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	ch.SetName("poller")
	if err := ch.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(fake.callsTo("deleteWebhook")) != 1 {
		t.Fatal("polling must clear any webhook first")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	msg, ok := msgBus.ConsumeInbound(ctx)
	if !ok || msg.Content != "hello" || msg.ChatID != "c1" || msg.SenderID != "u1" {
		t.Fatalf("got %+v (ok=%v)", msg, ok)
	}

	if err := ch.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := ch.Stop(context.Background()); err != nil {
		t.Fatal("second Stop must be a no-op")
	}
}

func TestStartFailsOnInvalidToken(t *testing.T) {
	fake := startFakeAPI(t)
	fake.handlers["getMe"] = func(map[string]any) string {
		return `{"ok":false,"error_code":401,"description":"invalid token"}`
	}
	ch, err := New(Config{Token: testToken}, bus.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ch.Start(context.Background()); err == nil {
		t.Fatal("expected Start to fail")
	}
	if ch.IsRunning() {
		t.Fatal("channel must not run after a failed start")
	}
}

func TestWebhookStartRegistersWithZalo(t *testing.T) {
	fake := startFakeAPI(t)
	fake.handlers["setWebhook"] = func(map[string]any) string {
		return `{"ok":true,"result":{"url":"https://example.com/hook","verification":{"ok":true,"outcome":"webhook.ok"}}}`
	}
	ch, err := New(Config{
		Token: testToken, ConnectionMode: modeWebhook,
		WebhookURL: "https://example.com" + WebhookPath, WebhookSecret: "start-secret",
	}, bus.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ch.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ch.Stop(context.Background()) })

	calls := fake.callsTo("setWebhook")
	if len(calls) != 1 || calls[0].Params["secret_token"] != "start-secret" {
		t.Fatalf("setWebhook calls = %+v", calls)
	}
	if router.lookup("start-secret") != ch {
		t.Fatal("channel not registered in the router")
	}
}
