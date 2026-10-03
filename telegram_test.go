package gridcore

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNewTelegram(t *testing.T) {
	if tg := NewTelegram("", "chat", time.Second); tg != nil {
		t.Fatal("empty token should return nil")
	}
	if tg := NewTelegram("token", "", time.Second); tg != nil {
		t.Fatal("empty chat id should return nil")
	}
	tg := NewTelegram("token", "chat", time.Second)
	if tg == nil || tg.apiBase != defaultTelegramAPI || tg.http == nil {
		t.Fatalf("unexpected telegram client: %+v", tg)
	}
}

func TestTelegramSendNilReceiver(t *testing.T) {
	var tg *Telegram
	if err := tg.Send(context.Background(), "hi"); err != nil {
		t.Fatalf("nil receiver Send = %v", err)
	}
}

func TestTelegramSendSuccess(t *testing.T) {
	var gotPath, gotBody, gotCT string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotCT = r.Header.Get("Content-Type")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	tg := &Telegram{token: "tok", chatID: "chat", apiBase: srv.URL, http: srv.Client()}
	if err := tg.Send(context.Background(), "hello world"); err != nil {
		t.Fatalf("Send = %v", err)
	}
	if gotPath != "/bottok/sendMessage" {
		t.Fatalf("path = %q", gotPath)
	}
	if !strings.HasPrefix(gotCT, "application/x-www-form-urlencoded") {
		t.Fatalf("content-type = %q", gotCT)
	}
	if !strings.Contains(gotBody, "chat_id=chat") || !strings.Contains(gotBody, "text=hello+world") {
		t.Fatalf("body = %q", gotBody)
	}
}

func TestTelegramSendNon2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	tg := &Telegram{token: "tok", chatID: "chat", apiBase: srv.URL, http: srv.Client()}
	err := tg.Send(context.Background(), "hello")
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Fatalf("expected 500 error, got %v", err)
	}
}

func TestTelegramSendEmptyBaseFallsBack(t *testing.T) {
	// apiBase empty → default; point http at a client whose transport always errors
	// so the default base is used but the request fails locally.
	tg := &Telegram{token: "tok", chatID: "chat", http: &http.Client{Transport: errTransport{}}}
	if err := tg.Send(context.Background(), "hi"); err == nil {
		t.Fatal("expected transport error")
	}
}

func TestTelegramSendBadRequest(t *testing.T) {
	tg := &Telegram{token: "bad\n", chatID: "chat", apiBase: "http://example.invalid", http: http.DefaultClient}
	if err := tg.Send(context.Background(), "hi"); err == nil {
		t.Fatal("expected malformed request error")
	}
}

func TestTelegramSendNilClientUsesDefault(t *testing.T) {
	var hit bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	tg := &Telegram{token: "tok", chatID: "chat", apiBase: srv.URL}
	if err := tg.Send(context.Background(), "hi"); err != nil {
		t.Fatalf("Send = %v", err)
	}
	if !hit {
		t.Fatal("expected default client to reach server")
	}
}

type errTransport struct{}

func (errTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, context.DeadlineExceeded
}
