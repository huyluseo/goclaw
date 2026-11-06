package zalobot

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/nextlevelbuilder/goclaw/internal/bus"
)

const testToken = "123:secret-bot-token"

type apiCall struct {
	Method string
	Params map[string]any
}

// fakeAPI records Bot API calls; handlers override per-method responses.
type fakeAPI struct {
	mu       sync.Mutex
	calls    []apiCall
	handlers map[string]func(params map[string]any) string
}

func (f *fakeAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	body, _ := io.ReadAll(r.Body)
	params := map[string]any{}
	_ = json.Unmarshal(body, &params)

	f.mu.Lock()
	f.calls = append(f.calls, apiCall{Method: method, Params: params})
	handler := f.handlers[method]
	f.mu.Unlock()

	if handler != nil {
		_, _ = io.WriteString(w, handler(params))
		return
	}
	_, _ = io.WriteString(w, `{"ok":true,"result":{}}`)
}

func (f *fakeAPI) callsTo(method string) []apiCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []apiCall
	for _, c := range f.calls {
		if c.Method == method {
			out = append(out, c)
		}
	}
	return out
}

func startFakeAPI(t *testing.T) *fakeAPI {
	t.Helper()
	fake := &fakeAPI{handlers: map[string]func(map[string]any) string{
		"getMe": func(map[string]any) string {
			return `{"ok":true,"result":{"id":"bot1","account_name":"Bot Demo","account_type":"BASIC"}}`
		},
		"getUpdates": func(map[string]any) string {
			return `{"ok":false,"error_code":408,"description":"no updates"}`
		},
	}}
	server := httptest.NewServer(fake)
	previous := apiBase
	apiBase = server.URL
	t.Cleanup(func() {
		apiBase = previous
		server.Close()
	})
	return fake
}

// newRunningChannel returns a channel marked running without network startup.
func newRunningChannel(t *testing.T, cfg Config) (*Channel, *bus.MessageBus) {
	t.Helper()
	if cfg.Token == "" {
		cfg.Token = testToken
	}
	if cfg.DMPolicy == "" {
		cfg.DMPolicy = "open"
	}
	if cfg.GroupPolicy == "" {
		cfg.GroupPolicy = "open"
	}
	msgBus := bus.New()
	ch, err := New(cfg, msgBus, nil, nil)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ch.SetName("zalo-test")
	ch.SetRunning(true)
	ch.botName = "Bot Demo"
	t.Cleanup(func() { _ = ch.Stop(t.Context()) })
	return ch, msgBus
}
