package zalobot

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestTransportErrorsNeverExposeToken(t *testing.T) {
	previous := apiBase
	apiBase = "http://127.0.0.1:1"
	t.Cleanup(func() { apiBase = previous })

	_, err := newAPIClient(testToken).getMe(context.Background())
	if err == nil {
		t.Fatal("expected a connection error")
	}
	if strings.Contains(err.Error(), testToken) {
		t.Fatalf("error leaks the bot token: %v", err)
	}
}

func TestAPIFailureBecomesAPIError(t *testing.T) {
	fake := startFakeAPI(t)
	fake.handlers["sendMessage"] = func(map[string]any) string {
		return `{"ok":false,"error_code":400,"description":"bad chat"}`
	}
	err := newAPIClient(testToken).sendMessage(context.Background(), "c1", "hi", "")
	var ae *apiError
	if !errors.As(err, &ae) || ae.Code != 400 || ae.Description != "bad chat" {
		t.Fatalf("got %v", err)
	}
}

func TestGetUpdatesTreats408AsNoUpdates(t *testing.T) {
	startFakeAPI(t)
	_, err := newAPIClient(testToken).getUpdates(context.Background(), 1)
	if !isNoUpdates(err) {
		t.Fatalf("got %v", err)
	}
}

func TestParseUpdatesShapes(t *testing.T) {
	const event = `{"event_name":"message.text.received","message":{"message_id":"m1","text":"hi"}}`
	tests := []struct {
		name string
		raw  string
		want int
	}{
		{"single event", event, 1},
		{"list", "[" + event + "," + event + "]", 2},
		{"webhook envelope", `{"ok":true,"result":` + event + `}`, 1},
		{"empty object", `{}`, 0},
		{"null", `null`, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseUpdates([]byte(tt.raw))
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != tt.want {
				t.Fatalf("got %d updates, want %d", len(got), tt.want)
			}
		})
	}
	if _, err := parseUpdates([]byte(`{broken`)); err == nil {
		t.Fatal("expected an error for invalid JSON")
	}
}

func TestSeenMessagesEvictsOldest(t *testing.T) {
	var seen seenMessages
	if !seen.markNew("a") || seen.markNew("a") {
		t.Fatal("duplicate not detected")
	}
	if !seen.markNew("") || !seen.markNew("") {
		t.Fatal("empty IDs must never be deduplicated")
	}
	for i := range seenMessagesCap {
		seen.markNew(string(rune(0x1000 + i)))
	}
	if !seen.markNew("a") {
		t.Fatal("oldest entry should have been evicted")
	}
}
