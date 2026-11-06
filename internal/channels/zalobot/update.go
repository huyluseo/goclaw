package zalobot

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

const (
	eventText        = "message.text.received"
	eventImage       = "message.image.received"
	eventSticker     = "message.sticker.received"
	eventVoice       = "message.voice.received"
	eventUnsupported = "message.unsupported.received"

	chatTypeGroup = "GROUP"
)

type sender struct {
	ID          string `json:"id"`
	DisplayName string `json:"display_name"`
	IsBot       bool   `json:"is_bot"`
}

type chat struct {
	ID       string `json:"id"`
	ChatType string `json:"chat_type"`
}

type message struct {
	MessageID string `json:"message_id"`
	From      sender `json:"from"`
	Chat      chat   `json:"chat"`
	Text      string `json:"text"`
	PhotoURL  string `json:"photo_url"`
	Caption   string `json:"caption"`
	Sticker   string `json:"sticker"`
	URL       string `json:"url"`
	VoiceURL  string `json:"voice_url"`
}

func (m *message) isGroup() bool { return strings.EqualFold(m.Chat.ChatType, chatTypeGroup) }

// update is one event, shared by getUpdates results and webhook bodies.
type update struct {
	EventName string  `json:"event_name"`
	Message   message `json:"message"`
}

// parseUpdates accepts a single event, a list of events, or either of those
// wrapped in the API's {ok, result} envelope. The docs show a single object for
// getUpdates, and tolerating the other shapes avoids silently losing messages.
func parseUpdates(raw []byte) ([]update, error) {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil, nil
	}
	if trimmed[0] == '[' {
		var list []update
		if err := json.Unmarshal(trimmed, &list); err != nil {
			return nil, fmt.Errorf("decode updates: %w", err)
		}
		out := list[:0]
		for _, u := range list {
			if u.EventName != "" {
				out = append(out, u)
			}
		}
		return out, nil
	}
	var wrapped struct {
		update
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(trimmed, &wrapped); err != nil {
		return nil, fmt.Errorf("decode update: %w", err)
	}
	if wrapped.EventName != "" {
		return []update{wrapped.update}, nil
	}
	if len(wrapped.Result) > 0 {
		return parseUpdates(wrapped.Result)
	}
	return nil, nil
}

// seenMessages remembers recent message IDs so a redelivered webhook event does
// not trigger a second agent run.
type seenMessages struct {
	mu    sync.Mutex
	ids   map[string]struct{}
	order []string
}

const seenMessagesCap = 1024

// markNew records id and reports whether it was not seen before.
func (s *seenMessages) markNew(id string) bool {
	if id == "" {
		return true
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ids == nil {
		s.ids = make(map[string]struct{}, seenMessagesCap)
	}
	if _, dup := s.ids[id]; dup {
		return false
	}
	if len(s.order) >= seenMessagesCap {
		delete(s.ids, s.order[0])
		s.order = s.order[1:]
	}
	s.ids[id] = struct{}{}
	s.order = append(s.order, id)
	return true
}
