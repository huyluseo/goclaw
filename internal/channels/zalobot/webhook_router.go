package zalobot

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	// RoutePrefix sits under /v1/ because the bundled nginx forwards only that
	// prefix to the gateway; anything else would land on the web UI.
	RoutePrefix = "/v1/channels/zalo_bot/"
	// WebhookPath is the URL path to use in an instance's webhook_url.
	WebhookPath = RoutePrefix + "webhook"

	mediaPathPrefix = RoutePrefix + "media/"

	secretHeader    = "X-Bot-Api-Secret-Token"
	maxWebhookBytes = 1 << 20
	mediaLinkTTL    = 15 * time.Minute
)

type mediaLink struct {
	path    string
	expires time.Time
}

// webhookRouter is the single HTTP entry point for every Zalo Bot instance.
// It exists independently of any instance so bots added at runtime work without
// a gateway restart: the mux cannot gain routes after startup.
type webhookRouter struct {
	mu        sync.RWMutex
	instances map[string]*Channel // webhook secret → channel
	media     map[string]mediaLink
}

var router = &webhookRouter{
	instances: make(map[string]*Channel),
	media:     make(map[string]mediaLink),
}

// Handler returns the shared handler to mount at RoutePrefix.
func Handler() http.Handler { return router }

// register binds a channel to its secret. Sharing a secret is refused: the
// secret is the only routing key, so events would reach the wrong bot or tenant.
func (r *webhookRouter) register(ch *Channel) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if existing, taken := r.instances[ch.cfg.WebhookSecret]; taken && existing != ch {
		return errors.New("zalo bot webhook_secret is already used by another instance")
	}
	r.instances[ch.cfg.WebhookSecret] = ch
	return nil
}

func (r *webhookRouter) unregister(ch *Channel) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.instances[ch.cfg.WebhookSecret] == ch {
		delete(r.instances, ch.cfg.WebhookSecret)
	}
}

// lookup compares against every secret in constant time so response timing does
// not reveal how much of a guess matched.
func (r *webhookRouter) lookup(secret string) *Channel {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var match *Channel
	for known, ch := range r.instances {
		if subtle.ConstantTimeCompare([]byte(known), []byte(secret)) == 1 {
			match = ch
		}
	}
	return match
}

func (r *webhookRouter) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	switch {
	case req.URL.Path == WebhookPath:
		r.serveWebhook(w, req)
	case strings.HasPrefix(req.URL.Path, mediaPathPrefix):
		r.serveMedia(w, req)
	default:
		http.NotFound(w, req)
	}
}

func (r *webhookRouter) serveWebhook(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	ch := r.lookup(req.Header.Get(secretHeader))
	if ch == nil {
		slog.Warn("security.zalo_bot_webhook_rejected", "remote_addr", req.RemoteAddr)
		w.WriteHeader(http.StatusForbidden)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, req.Body, maxWebhookBytes))
	if err != nil {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		return
	}
	updates, err := parseUpdates(body)
	if err != nil {
		slog.Warn("zalo_bot: invalid webhook body", "channel", ch.Name(), "error", err)
		w.WriteHeader(http.StatusBadRequest)
		return
	}

	// An authenticated delivery proves the webhook works, even if every startup
	// verification ran before the listener was up.
	ch.MarkHealthy("Connected (webhook)")

	// Zalo expects a fast 2xx, while handling may download media.
	go func() {
		for _, u := range updates {
			ch.processUpdate(u)
		}
	}()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"message": "Success"})
}

// publishMedia issues a short-lived unguessable link for one local file. The
// token keeps the file extension so the URL still looks like the file it serves.
func (r *webhookRouter) publishMedia(path string) (string, error) {
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("attachment unavailable: %w", err)
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate media token: %w", err)
	}
	token := base64.RawURLEncoding.EncodeToString(raw) + strings.ToLower(filepath.Ext(path))

	now := time.Now()
	r.mu.Lock()
	defer r.mu.Unlock()
	for key, link := range r.media {
		if now.After(link.expires) {
			delete(r.media, key)
		}
	}
	r.media[token] = mediaLink{path: path, expires: now.Add(mediaLinkTTL)}
	return token, nil
}

func (r *webhookRouter) serveMedia(w http.ResponseWriter, req *http.Request) {
	if req.Method != http.MethodGet && req.Method != http.MethodHead {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	token := strings.TrimPrefix(req.URL.Path, mediaPathPrefix)
	r.mu.RLock()
	link, ok := r.media[token]
	r.mu.RUnlock()
	if !ok || time.Now().After(link.expires) {
		http.NotFound(w, req)
		return
	}
	file, err := os.Open(link.path)
	if err != nil {
		http.NotFound(w, req)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.IsDir() {
		http.NotFound(w, req)
		return
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	http.ServeContent(w, req, filepath.Base(link.path), info.ModTime(), file)
}
