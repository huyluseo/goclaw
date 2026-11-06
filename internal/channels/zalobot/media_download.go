package zalobot

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"strings"
	"time"

	"github.com/nextlevelbuilder/goclaw/internal/tools"
)

const downloadTimeout = 60 * time.Second

var mediaHTTPClient = &http.Client{
	Timeout: downloadTimeout,
	// A public URL must not be able to redirect the download to an internal host.
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		return checkMediaURL(req.URL.String())
	},
}

// checkMediaURL is the SSRF guard for inbound media; tests replace it because
// their fixture server runs on loopback.
var checkMediaURL = tools.CheckSSRF

// downloadFile saves fileURL to a temp file, rejecting anything over maxBytes.
func downloadFile(ctx context.Context, fileURL string, maxBytes int64) (string, error) {
	if err := checkMediaURL(fileURL); err != nil {
		return "", fmt.Errorf("ssrf check: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fileURL, nil)
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	resp, err := mediaHTTPClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download status %d", resp.StatusCode)
	}

	tmp, err := os.CreateTemp("", "goclaw_zalobot_*"+mediaExtension(fileURL, resp.Header.Get("Content-Type")))
	if err != nil {
		return "", fmt.Errorf("create temp: %w", err)
	}
	defer tmp.Close()

	written, err := io.Copy(tmp, io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		os.Remove(tmp.Name())
		return "", fmt.Errorf("save: %w", err)
	}
	if written == 0 || written > maxBytes {
		os.Remove(tmp.Name())
		return "", fmt.Errorf("unacceptable file size: %d bytes (max %d)", written, maxBytes)
	}
	return tmp.Name(), nil
}

// mediaExtension prefers the Content-Type over the URL path because the agent
// pipeline classifies files by extension.
func mediaExtension(fileURL, contentType string) string {
	ct := strings.ToLower(contentType)
	for _, known := range []struct{ marker, ext string }{
		{"png", ".png"}, {"gif", ".gif"}, {"webp", ".webp"}, {"jpeg", ".jpg"}, {"jpg", ".jpg"},
		{"aac", ".aac"}, {"mpeg", ".mp3"}, {"mp4", ".m4a"}, {"ogg", ".ogg"}, {"wav", ".wav"},
	} {
		if strings.Contains(ct, known.marker) {
			return known.ext
		}
	}
	clean, _, _ := strings.Cut(fileURL, "?")
	if ext := path.Ext(clean); ext != "" && len(ext) <= 5 {
		return ext
	}
	return ".bin"
}
