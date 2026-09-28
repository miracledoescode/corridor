// Package notify delivers alerts to subscribers.
//
// WHY no telegram library: the whole job here is one endpoint —
// POST /bot<token>/sendMessage. A client for that is a few dozen lines of
// net/http, against a dependency that would pull in a whole bot framework
// (routing, update polling, middleware) for a feature this package does not
// have. Stdlib-first, and one fewer thing to keep patched. Revisit if and
// when interactive commands like /event arrive, which is what a framework is
// actually good for.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"
)

const telegramAPI = "https://api.telegram.org"

// Telegram sends messages as a bot.
type Telegram struct {
	token   string
	baseURL string
	http    *http.Client
	log     *slog.Logger
}

func NewTelegram(token string, log *slog.Logger) *Telegram {
	return &Telegram{
		token:   token,
		baseURL: telegramAPI,
		// A bounded client: a hung Telegram call must not wedge the dispatch
		// loop, and an alert that arrives minutes late is worthless anyway.
		http: &http.Client{Timeout: 10 * time.Second},
		log:  log,
	}
}

type sendMessageRequest struct {
	ChatID                string `json:"chat_id"`
	Text                  string `json:"text"`
	ParseMode             string `json:"parse_mode"`
	DisableWebPagePreview bool   `json:"disable_web_page_preview"`
}

type telegramResponse struct {
	OK          bool   `json:"ok"`
	Description string `json:"description"`
}

// Send delivers text to one chat.
//
// WHY no error here ever contains the URL: the bot token is IN the path
// (/bot<token>/sendMessage), so the error strings net/http produces embed the
// credential. Returning those verbatim would write the token into the logs on
// every transient network blip — a secret leaked by an error path. Every
// failure below is therefore constructed by hand.
func (t *Telegram) Send(ctx context.Context, chatID, text string) error {
	body, err := json.Marshal(sendMessageRequest{
		ChatID:                chatID,
		Text:                  text,
		ParseMode:             "HTML",
		DisableWebPagePreview: true,
	})
	if err != nil {
		return fmt.Errorf("encode telegram request: %w", err)
	}

	url := t.baseURL + "/bot" + t.token + "/sendMessage"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build telegram request for chat %s", chatID)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := t.http.Do(req)
	if err != nil {
		return fmt.Errorf("telegram send to chat %s failed", chatID)
	}
	defer resp.Body.Close()

	var tr telegramResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return fmt.Errorf("telegram send to chat %s: bad response (http %d)", chatID, resp.StatusCode)
	}
	if !tr.OK {
		// Telegram's own description is safe: it describes the chat/message,
		// never the token.
		return fmt.Errorf("telegram rejected send to chat %s: %s", chatID, tr.Description)
	}
	return nil
}
