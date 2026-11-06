package zalobot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// apiBase is the Zalo Bot Platform root. Declared as a variable so tests can
// point it at an httptest server.
var apiBase = "https://bot-api.zaloplatforms.com"

const (
	apiTimeout          = 30 * time.Second
	maxAPIResponseBytes = 1 << 20
	noUpdatesErrorCode  = 408
)

// apiError is a failure reported by the Bot API itself. It never carries the
// request URL, because the bot token is part of that URL.
type apiError struct {
	Method      string
	Code        int
	Description string
}

func (e *apiError) Error() string {
	return fmt.Sprintf("zalo bot %s failed: code=%d: %s", e.Method, e.Code, e.Description)
}

func isNoUpdates(err error) bool {
	var ae *apiError
	return errors.As(err, &ae) && ae.Code == noUpdatesErrorCode
}

type apiResponse struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result,omitempty"`
	ErrorCode   int             `json:"error_code,omitempty"`
	Description string          `json:"description,omitempty"`
}

type botInfo struct {
	ID            string `json:"id"`
	AccountName   string `json:"account_name"`
	AccountType   string `json:"account_type"`
	CanJoinGroups bool   `json:"can_join_groups"`
}

// webhookVerification is the delivery probe Zalo runs during setWebhook. The URL
// is saved even when the probe fails, so callers must inspect it.
type webhookVerification struct {
	OK      bool   `json:"ok"`
	Outcome string `json:"outcome"`
	Hint    string `json:"hint"`
}

type webhookResult struct {
	URL          string               `json:"url"`
	Verification *webhookVerification `json:"verification"`
}

// apiClient calls the Bot API. pollHTTP has no client timeout because long polls
// are bounded by their context instead.
type apiClient struct {
	token    string
	http     *http.Client
	pollHTTP *http.Client
}

func newAPIClient(token string) *apiClient {
	return &apiClient{
		token:    token,
		http:     &http.Client{Timeout: apiTimeout},
		pollHTTP: &http.Client{},
	}
}

func (a *apiClient) call(ctx context.Context, client *http.Client, method string, params any) (json.RawMessage, error) {
	body, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("zalo bot %s: encode request: %w", method, err)
	}
	endpoint := apiBase + "/bot" + a.token + "/" + method
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("zalo bot %s: build request: %w", method, redactURLError(err))
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("zalo bot %s: %w", method, redactURLError(err))
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxAPIResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("zalo bot %s: read response: %w", method, redactURLError(err))
	}
	var parsed apiResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, &apiError{Method: method, Code: resp.StatusCode, Description: "invalid response body"}
	}
	if !parsed.OK {
		code := parsed.ErrorCode
		if code == 0 {
			code = resp.StatusCode
		}
		return nil, &apiError{Method: method, Code: code, Description: parsed.Description}
	}
	return parsed.Result, nil
}

// redactURLError drops the *url.Error wrapper, whose message embeds the full
// request URL and therefore the bot token.
func redactURLError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}

func (a *apiClient) getMe(ctx context.Context) (*botInfo, error) {
	raw, err := a.call(ctx, a.http, "getMe", struct{}{})
	if err != nil {
		return nil, err
	}
	var info botInfo
	if err := json.Unmarshal(raw, &info); err != nil {
		return nil, fmt.Errorf("zalo bot getMe: decode result: %w", err)
	}
	return &info, nil
}

func (a *apiClient) getUpdates(ctx context.Context, timeoutSeconds int) ([]update, error) {
	raw, err := a.call(ctx, a.pollHTTP, "getUpdates", map[string]any{"timeout": timeoutSeconds})
	if err != nil {
		return nil, err
	}
	return parseUpdates(raw)
}

func (a *apiClient) setWebhook(ctx context.Context, webhookURL, secret string) (*webhookResult, error) {
	raw, err := a.call(ctx, a.http, "setWebhook", map[string]any{"url": webhookURL, "secret_token": secret})
	if err != nil {
		return nil, err
	}
	var result webhookResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil, fmt.Errorf("zalo bot setWebhook: decode result: %w", err)
	}
	return &result, nil
}

func (a *apiClient) deleteWebhook(ctx context.Context) error {
	_, err := a.call(ctx, a.http, "deleteWebhook", struct{}{})
	return err
}

func (a *apiClient) sendMessage(ctx context.Context, chatID, text, parseMode string) error {
	params := map[string]any{"chat_id": chatID, "text": text}
	if parseMode != "" {
		params["parse_mode"] = parseMode
	}
	_, err := a.call(ctx, a.http, "sendMessage", params)
	return err
}

func (a *apiClient) sendPhoto(ctx context.Context, chatID, photoURL, caption string) error {
	params := map[string]any{"chat_id": chatID, "photo": photoURL}
	if caption != "" {
		params["caption"] = caption
	}
	_, err := a.call(ctx, a.http, "sendPhoto", params)
	return err
}

func (a *apiClient) sendVoice(ctx context.Context, chatID, voiceURL string) error {
	_, err := a.call(ctx, a.http, "sendVoice", map[string]any{"chat_id": chatID, "voice_url": voiceURL})
	return err
}

func (a *apiClient) sendTyping(ctx context.Context, chatID string) error {
	_, err := a.call(ctx, a.http, "sendChatAction", map[string]any{"chat_id": chatID, "action": "typing"})
	return err
}
