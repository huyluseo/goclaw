package zalobot

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
	"github.com/nextlevelbuilder/goclaw/internal/channels/media"
)

func textUpdate(id, chatType, text string) update {
	return update{EventName: eventText, Message: message{
		MessageID: id,
		Text:      text,
		From:      sender{ID: "u1", DisplayName: "An"},
		Chat:      chat{ID: "c1", ChatType: chatType},
	}}
}

func consumeOne(t *testing.T, msgBus *bus.MessageBus) (bus.InboundMessage, bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	return msgBus.ConsumeInbound(ctx)
}

func TestDirectMessageReachesAgent(t *testing.T) {
	startFakeAPI(t)
	ch, msgBus := newRunningChannel(t, Config{})
	ch.processUpdate(textUpdate("m1", "PRIVATE", "hello"))

	msg, ok := consumeOne(t, msgBus)
	if !ok || msg.Content != "hello" || msg.PeerKind != "direct" {
		t.Fatalf("got %+v (ok=%v)", msg, ok)
	}
}

func TestDuplicateAndBotMessagesAreDropped(t *testing.T) {
	startFakeAPI(t)
	ch, msgBus := newRunningChannel(t, Config{})
	ch.processUpdate(textUpdate("m1", "PRIVATE", "first"))
	if _, ok := consumeOne(t, msgBus); !ok {
		t.Fatal("first delivery missing")
	}
	ch.processUpdate(textUpdate("m1", "PRIVATE", "redelivered"))

	fromBot := textUpdate("m2", "PRIVATE", "echo")
	fromBot.Message.From.IsBot = true
	ch.processUpdate(fromBot)

	if msg, ok := consumeOne(t, msgBus); ok {
		t.Fatalf("unexpected message: %+v", msg)
	}
}

func TestDisabledDMPolicyBlocks(t *testing.T) {
	startFakeAPI(t)
	ch, msgBus := newRunningChannel(t, Config{DMPolicy: "disabled"})
	ch.processUpdate(textUpdate("m1", "PRIVATE", "hello"))
	if msg, ok := consumeOne(t, msgBus); ok {
		t.Fatalf("unexpected message: %+v", msg)
	}
}

func TestGroupMentionGateKeepsContext(t *testing.T) {
	startFakeAPI(t)
	ch, msgBus := newRunningChannel(t, Config{RequireMention: true, HistoryLimit: 10})

	ch.processUpdate(textUpdate("m1", "GROUP", "background chatter"))
	if msg, ok := consumeOne(t, msgBus); ok {
		t.Fatalf("unmentioned message must not reach the agent: %+v", msg)
	}

	ch.processUpdate(textUpdate("m2", "GROUP", "@bot demo please help"))
	msg, ok := consumeOne(t, msgBus)
	if !ok {
		t.Fatal("mentioned message missing")
	}
	if msg.PeerKind != "group" || msg.Metadata["group_id"] != "c1" {
		t.Fatalf("got %+v", msg)
	}
	if !strings.Contains(msg.Content, "background chatter") || !strings.Contains(msg.Content, "[From: An]") {
		t.Fatalf("group context missing: %q", msg.Content)
	}
}

func TestGroupMentionMatchesConfiguredNames(t *testing.T) {
	startFakeAPI(t)
	ch, msgBus := newRunningChannel(t, Config{RequireMention: true, MentionNames: []string{" @Bot Mao Trung ", ""}})

	ch.processUpdate(textUpdate("m1", "GROUP", "@Bot Mao Trung báo cáo tháng này"))
	if _, ok := consumeOne(t, msgBus); !ok {
		t.Fatal("message tagging a configured name must reach the agent")
	}

	ch.processUpdate(textUpdate("m2", "GROUP", "Bot Mao Trung without the at sign"))
	if msg, ok := consumeOne(t, msgBus); ok {
		t.Fatalf("name without @ must not count as a mention: %+v", msg)
	}
}

func TestUnsupportedEventIgnored(t *testing.T) {
	startFakeAPI(t)
	ch, msgBus := newRunningChannel(t, Config{})
	u := textUpdate("m1", "PRIVATE", "x")
	u.EventName = eventUnsupported
	ch.processUpdate(u)
	if msg, ok := consumeOne(t, msgBus); ok {
		t.Fatalf("unexpected message: %+v", msg)
	}
}

func allowLoopbackMedia(t *testing.T) {
	t.Helper()
	previous := checkMediaURL
	checkMediaURL = func(string) error { return nil }
	t.Cleanup(func() { checkMediaURL = previous })
}

func TestImageIsDownloadedAndTagged(t *testing.T) {
	startFakeAPI(t)
	allowLoopbackMedia(t)
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("png-bytes"))
	}))
	t.Cleanup(cdn.Close)

	ch, msgBus := newRunningChannel(t, Config{})
	ch.processUpdate(update{EventName: eventImage, Message: message{
		MessageID: "m1", PhotoURL: cdn.URL + "/photo", Caption: "look",
		From: sender{ID: "u1"}, Chat: chat{ID: "c1", ChatType: "PRIVATE"},
	}})

	msg, ok := consumeOne(t, msgBus)
	if !ok || len(msg.Media) != 1 {
		t.Fatalf("got %+v (ok=%v)", msg, ok)
	}
	t.Cleanup(func() { os.Remove(msg.Media[0].Path) })
	if !strings.HasSuffix(msg.Media[0].Path, ".png") {
		t.Fatalf("media path %q lost its type", msg.Media[0].Path)
	}
	if !strings.Contains(msg.Content, "look") || !strings.Contains(msg.Content, "<media:image>") {
		t.Fatalf("content = %q", msg.Content)
	}
}

func TestDownloadRejectsOversizedFile(t *testing.T) {
	allowLoopbackMedia(t)
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(make([]byte, 64))
	}))
	t.Cleanup(cdn.Close)
	if _, err := downloadFile(context.Background(), cdn.URL, 16); err == nil {
		t.Fatal("expected size rejection")
	}
}

func TestDownloadBlocksInternalAddresses(t *testing.T) {
	if _, err := downloadFile(context.Background(), "http://127.0.0.1:9/secret", 1024); err == nil {
		t.Fatal("expected SSRF rejection")
	}
}

func TestDownloadBlocksRedirectToInternalAddress(t *testing.T) {
	previous := checkMediaURL
	checkMediaURL = func(u string) error {
		if strings.Contains(u, "/internal") {
			return os.ErrPermission
		}
		return nil
	}
	t.Cleanup(func() { checkMediaURL = previous })

	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/internal" {
			t.Error("redirect target must not be fetched")
			return
		}
		http.Redirect(w, r, "/internal", http.StatusFound)
	}))
	t.Cleanup(cdn.Close)
	if _, err := downloadFile(context.Background(), cdn.URL+"/start", 1024); err == nil {
		t.Fatal("expected redirect rejection")
	}
}

func TestVoiceAttachmentIsTaggedAsAudio(t *testing.T) {
	startFakeAPI(t)
	allowLoopbackMedia(t)
	cdn := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "audio/aac")
		_, _ = w.Write([]byte("aac-bytes"))
	}))
	t.Cleanup(cdn.Close)

	ch, msgBus := newRunningChannel(t, Config{})
	ch.processUpdate(update{EventName: eventVoice, Message: message{
		MessageID: "m1", VoiceURL: cdn.URL + "/voice.aac",
		From: sender{ID: "u1"}, Chat: chat{ID: "c1", ChatType: "PRIVATE"},
	}})

	msg, ok := consumeOne(t, msgBus)
	if !ok || len(msg.Media) != 1 {
		t.Fatalf("got %+v (ok=%v)", msg, ok)
	}
	t.Cleanup(func() { os.Remove(msg.Media[0].Path) })
	// The exact subtype depends on the host MIME table; the kind must not.
	if got := media.MediaKindFromMime(media.DetectMIMEType(msg.Media[0].Path)); got != "audio" {
		t.Fatalf("kind = %q", got)
	}
}
