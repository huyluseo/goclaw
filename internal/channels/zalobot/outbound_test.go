package zalobot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
)

func TestSendChunksLongText(t *testing.T) {
	fake := startFakeAPI(t)
	ch, _ := newRunningChannel(t, Config{})

	err := ch.Send(context.Background(), bus.OutboundMessage{ChatID: "c1", Content: strings.Repeat("word ", 900)})
	if err != nil {
		t.Fatal(err)
	}
	calls := fake.callsTo("sendMessage")
	if len(calls) < 2 {
		t.Fatalf("expected several chunks, got %d", len(calls))
	}
	for _, call := range calls {
		if n := utf8.RuneCountInString(call.Params["text"].(string)); n > maxTextLength {
			t.Fatalf("chunk of %d runes exceeds the limit", n)
		}
		if call.Params["parse_mode"] != parseMarkdown {
			t.Fatalf("parse_mode = %v", call.Params["parse_mode"])
		}
	}
}

func TestSendFallsBackToPlainText(t *testing.T) {
	fake := startFakeAPI(t)
	fake.handlers["sendMessage"] = func(params map[string]any) string {
		if params["parse_mode"] != nil {
			return `{"ok":false,"error_code":400,"description":"bad markdown"}`
		}
		return `{"ok":true,"result":{}}`
	}
	ch, _ := newRunningChannel(t, Config{})

	if err := ch.Send(context.Background(), bus.OutboundMessage{ChatID: "c1", Content: "**bold**"}); err != nil {
		t.Fatal(err)
	}
	calls := fake.callsTo("sendMessage")
	if len(calls) != 2 || calls[1].Params["text"] != "bold" {
		t.Fatalf("calls = %+v", calls)
	}
}

func TestSendFailsWhenNotRunning(t *testing.T) {
	ch, err := New(Config{Token: testToken}, bus.New(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := ch.Send(context.Background(), bus.OutboundMessage{ChatID: "c1", Content: "x"}); err == nil {
		t.Fatal("expected an error")
	}
}

func TestSendRemotePhotoPassesURLThrough(t *testing.T) {
	fake := startFakeAPI(t)
	ch, _ := newRunningChannel(t, Config{})
	err := ch.Send(context.Background(), bus.OutboundMessage{ChatID: "c1", Media: []bus.MediaAttachment{
		{URL: "https://cdn.example.com/a.png", ContentType: "image/png", Caption: "cap"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	calls := fake.callsTo("sendPhoto")
	if len(calls) != 1 || calls[0].Params["photo"] != "https://cdn.example.com/a.png" || calls[0].Params["caption"] != "cap" {
		t.Fatalf("calls = %+v", calls)
	}
}

func TestSendLocalPhotoIsServedThroughGateway(t *testing.T) {
	fake := startFakeAPI(t)
	ch, _ := newRunningChannel(t, Config{PublicURL: func() string { return "https://gw.example.com/" }})

	file := filepath.Join(t.TempDir(), "chart.png")
	if err := os.WriteFile(file, []byte("png-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := ch.Send(context.Background(), bus.OutboundMessage{ChatID: "c1", Media: []bus.MediaAttachment{{URL: file, ContentType: "image/png"}}})
	if err != nil {
		t.Fatal(err)
	}
	calls := fake.callsTo("sendPhoto")
	if len(calls) != 1 {
		t.Fatalf("calls = %+v", calls)
	}
	photoURL := calls[0].Params["photo"].(string)
	const prefix = "https://gw.example.com" + mediaPathPrefix
	if !strings.HasPrefix(photoURL, prefix) || strings.Contains(photoURL, "chart") {
		t.Fatalf("photo URL = %q", photoURL)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, strings.TrimPrefix(photoURL, "https://gw.example.com"), nil)
	Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || rec.Body.String() != "png-bytes" {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
}

func TestSendLocalPhotoWithoutPublicURLStillSendsText(t *testing.T) {
	fake := startFakeAPI(t)
	ch, _ := newRunningChannel(t, Config{})
	err := ch.Send(context.Background(), bus.OutboundMessage{ChatID: "c1", Content: "caption text", Media: []bus.MediaAttachment{
		{URL: "/tmp/a.png", ContentType: "image/png"},
	}})
	if err == nil {
		t.Fatal("the undelivered photo must be reported")
	}
	if len(fake.callsTo("sendMessage")) != 1 {
		t.Fatal("text must still be delivered")
	}
}

func TestVoiceIsRefusedInGroups(t *testing.T) {
	fake := startFakeAPI(t)
	ch, _ := newRunningChannel(t, Config{})
	ch.groupChats.Store("g1", true)
	voice := []bus.MediaAttachment{{URL: "https://cdn.example.com/v.aac", ContentType: "audio/aac"}}

	if err := ch.Send(context.Background(), bus.OutboundMessage{ChatID: "g1", Media: voice}); err == nil {
		t.Fatal("expected refusal in a group")
	}
	if err := ch.Send(context.Background(), bus.OutboundMessage{ChatID: "c1", Media: voice}); err != nil {
		t.Fatal(err)
	}
	if calls := fake.callsTo("sendVoice"); len(calls) != 1 || calls[0].Params["chat_id"] != "c1" {
		t.Fatalf("calls = %+v", calls)
	}
}

func TestUnsupportedAttachmentIsReported(t *testing.T) {
	startFakeAPI(t)
	ch, _ := newRunningChannel(t, Config{})
	err := ch.Send(context.Background(), bus.OutboundMessage{ChatID: "c1", Media: []bus.MediaAttachment{
		{URL: "https://cdn.example.com/a.pdf", ContentType: "application/pdf"},
	}})
	if err == nil {
		t.Fatal("expected an error")
	}
}

func TestCompactListSpacing(t *testing.T) {
	cases := map[string]struct{ input, want string }{
		"paragraph then bullets": {"Intro\n\n- a\n- b", "Intro\n- a\n- b"},
		"several blank lines":    {"Intro\n\n\n- a", "Intro\n- a"},
		"loose list":             {"- a\n\n- b", "- a\n- b"},
		"ordered list":           {"Intro\n\n1. a\n\n2. b", "Intro\n1. a\n2. b"},
		"ordered from three":     {"Intro\n\n3. a", "Intro\n\n3. a"},
		"paragraph after list":   {"- a\n\nOutro", "- a\n\nOutro"},
		"plain paragraphs":       {"One\n\nTwo", "One\n\nTwo"},
		"code block":             {"```\nx\n\n- y\n```", "```\nx\n\n- y\n```"},
	}
	for name, tc := range cases {
		if got := compactListSpacing(tc.input); got != tc.want {
			t.Errorf("%s: got %q, want %q", name, got, tc.want)
		}
	}
}

func TestSendRemovesBlankLineBeforeList(t *testing.T) {
	fake := startFakeAPI(t)
	ch, _ := newRunningChannel(t, Config{})

	err := ch.Send(context.Background(), bus.OutboundMessage{ChatID: "c1", Content: "**Intro**\n\n- a\n- b"})
	if err != nil {
		t.Fatal(err)
	}
	calls := fake.callsTo("sendMessage")
	if len(calls) != 1 || calls[0].Params["text"] != "**Intro**\n- a\n- b" {
		t.Fatalf("calls = %+v", calls)
	}
}

func TestSendEndsLongTextChunksAtSentences(t *testing.T) {
	fake := startFakeAPI(t)
	ch, _ := newRunningChannel(t, Config{})

	err := ch.Send(context.Background(), bus.OutboundMessage{ChatID: "c1", Content: strings.TrimSpace(strings.Repeat("Doan van dai de thu chia tin. ", 200))})
	if err != nil {
		t.Fatal(err)
	}
	calls := fake.callsTo("sendMessage")
	if len(calls) < 2 {
		t.Fatalf("expected several chunks, got %d", len(calls))
	}
	for _, call := range calls {
		if text := call.Params["text"].(string); !strings.HasSuffix(text, "chia tin.") {
			t.Fatalf("chunk ends mid-sentence: %q", text[len(text)-30:])
		}
	}
}
