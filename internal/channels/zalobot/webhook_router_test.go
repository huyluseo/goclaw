package zalobot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
)

const webhookBody = `{"event_name":"message.text.received","message":{"message_id":"w1","text":"hi","from":{"id":"u1"},"chat":{"id":"c1","chat_type":"PRIVATE"}}}`

func webhookChannel(t *testing.T, secret string) (*Channel, *bus.MessageBus) {
	t.Helper()
	ch, msgBus := newRunningChannel(t, Config{
		ConnectionMode: modeWebhook,
		WebhookURL:     "https://example.com" + WebhookPath,
		WebhookSecret:  secret,
	})
	if err := router.register(ch); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { router.unregister(ch) })
	return ch, msgBus
}

func postWebhook(secret, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, WebhookPath, strings.NewReader(body))
	if secret != "" {
		req.Header.Set(secretHeader, secret)
	}
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, req)
	return rec
}

func TestWebhookRoutesEventToOwningBot(t *testing.T) {
	startFakeAPI(t)
	_, busA := webhookChannel(t, "secret-for-a")
	_, busB := webhookChannel(t, "secret-for-b")

	if rec := postWebhook("secret-for-b", webhookBody); rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if msg, ok := busB.ConsumeInbound(ctx); !ok || msg.Content != "hi" {
		t.Fatalf("got %+v (ok=%v)", msg, ok)
	}
	if msg, ok := consumeOne(t, busA); ok {
		t.Fatalf("event leaked to another bot: %+v", msg)
	}
}

func TestWebhookRejectsBadRequests(t *testing.T) {
	startFakeAPI(t)
	webhookChannel(t, "good-secret")

	tests := []struct {
		name   string
		secret string
		body   string
		want   int
	}{
		{"missing secret", "", webhookBody, http.StatusForbidden},
		{"wrong secret", "bad-secret", webhookBody, http.StatusForbidden},
		{"invalid json", "good-secret", "{nope", http.StatusBadRequest},
		{"oversized body", "good-secret", strings.Repeat("x", maxWebhookBytes+1), http.StatusRequestEntityTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if rec := postWebhook(tt.secret, tt.body); rec.Code != tt.want {
				t.Fatalf("status = %d, want %d", rec.Code, tt.want)
			}
		})
	}

	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, WebhookPath, nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET status = %d", rec.Code)
	}
}

func TestRouterRefusesSharedSecret(t *testing.T) {
	startFakeAPI(t)
	first, _ := webhookChannel(t, "shared-secret")
	second, _ := newRunningChannel(t, Config{
		ConnectionMode: modeWebhook,
		WebhookURL:     "https://example.com" + WebhookPath,
		WebhookSecret:  "shared-secret",
	})
	if err := router.register(second); err == nil {
		t.Fatal("expected a duplicate secret to be refused")
	}
	if router.lookup("shared-secret") != first {
		t.Fatal("the original owner must keep the secret")
	}
	router.unregister(second)
	if router.lookup("shared-secret") != first {
		t.Fatal("a non-owner must not remove the registration")
	}
}

func TestMediaLinkExpiresAndRejectsUnknownTokens(t *testing.T) {
	file := filepath.Join(t.TempDir(), "a.png")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	token, err := router.publishMedia(file)
	if err != nil {
		t.Fatal(err)
	}

	get := func(path string) int {
		rec := httptest.NewRecorder()
		Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec.Code
	}
	if code := get(mediaPathPrefix + token); code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	if code := get(mediaPathPrefix + "unknown.png"); code != http.StatusNotFound {
		t.Fatalf("status = %d", code)
	}

	router.mu.Lock()
	link := router.media[token]
	link.expires = time.Now().Add(-time.Second)
	router.media[token] = link
	router.mu.Unlock()
	if code := get(mediaPathPrefix + token); code != http.StatusNotFound {
		t.Fatalf("expired link status = %d", code)
	}
}

func TestPublishMediaRequiresExistingFile(t *testing.T) {
	if _, err := router.publishMedia(filepath.Join(t.TempDir(), "missing.png")); err == nil {
		t.Fatal("expected an error")
	}
}
