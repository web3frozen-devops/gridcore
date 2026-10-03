package gridcore

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// defaultTelegramAPI is the Telegram Bot API base URL.
const defaultTelegramAPI = "https://api.telegram.org"

type Telegram struct {
	token  string
	chatID string
	// apiBase overrides the Telegram API base URL (used in tests).
	apiBase string
	http    *http.Client
}

func NewTelegram(token, chatID string, timeout time.Duration) *Telegram {
	if token == "" || chatID == "" {
		return nil
	}
	return &Telegram{token: token, chatID: chatID, apiBase: defaultTelegramAPI, http: &http.Client{Timeout: timeout}}
}

func (t *Telegram) Send(ctx context.Context, text string) error {
	if t == nil {
		return nil
	}
	form := url.Values{}
	form.Set("chat_id", t.chatID)
	form.Set("text", text)
	base := t.apiBase
	if base == "" {
		base = defaultTelegramAPI
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/bot"+t.token+"/sendMessage", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	client := t.http
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("telegram send failed: %s", resp.Status)
	}
	return nil
}
