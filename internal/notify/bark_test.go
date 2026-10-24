package notify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestBarkSendDeliversBoundedPayload(t *testing.T) {
	var payload map[string]any
	var requestPath string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestPath = r.URL.Path
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read body: %v", err)
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Errorf("payload is not JSON: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":200,"message":"success"}`))
	}))
	defer server.Close()

	channel, err := NewBark(BarkConfig{
		Server:    server.URL,
		DeviceKey: "device-key-1",
		Group:     "Folio",
		Level:     "timeSensitive",
	})
	if err != nil {
		t.Fatalf("new bark: %v", err)
	}
	receipt := channel.Send(context.Background(), Message{
		Title: "Run done",
		Body:  strings.Repeat("x", MaxBodyBytes+500),
		URL:   "https://example.com/runs/1",
	})
	if receipt.Status != StatusDelivered {
		t.Fatalf("receipt=%+v want delivered", receipt)
	}
	// The device key is a credential: it belongs in the body, never the URL,
	// because URLs are logged by proxies.
	if requestPath != "/push" {
		t.Fatalf("request path=%q want /push", requestPath)
	}
	if payload["device_key"] != "device-key-1" {
		t.Fatalf("device_key=%v", payload["device_key"])
	}
	if payload["level"] != "timeSensitive" {
		t.Fatalf("level=%v", payload["level"])
	}
	if payload["group"] != "Folio" {
		t.Fatalf("group=%v", payload["group"])
	}
	if payload["isArchive"] != true {
		t.Fatalf("isArchive=%v", payload["isArchive"])
	}
	body, _ := payload["body"].(string)
	if len(body) > MaxBodyBytes {
		t.Fatalf("body bytes=%d want <= %d", len(body), MaxBodyBytes)
	}
}

func TestBarkReportsApplicationErrorAsFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Bark returns HTTP 200 with a non-200 code for application errors.
		_, _ = w.Write([]byte(`{"code":400,"message":"failed to get device token"}`))
	}))
	defer server.Close()
	channel, err := NewBark(BarkConfig{Server: server.URL, DeviceKey: "key-1"})
	if err != nil {
		t.Fatalf("new bark: %v", err)
	}
	receipt := channel.Send(context.Background(), Message{Title: "t", Body: "b"})
	if receipt.Status != StatusFailed || receipt.Detail != "code_400" {
		t.Fatalf("receipt=%+v want failed/code_400", receipt)
	}
}

func TestBarkRefusesRedirect(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":200}`))
	}))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/push", http.StatusFound)
	}))
	defer server.Close()
	channel, err := NewBark(BarkConfig{Server: server.URL, DeviceKey: "key-1"})
	if err != nil {
		t.Fatalf("new bark: %v", err)
	}
	receipt := channel.Send(context.Background(), Message{Title: "t", Body: "b"})
	if receipt.Status != StatusFailed || receipt.Detail != "http_302" {
		t.Fatalf("receipt=%+v want failed/http_302", receipt)
	}
}

func TestBarkConfigValidation(t *testing.T) {
	tests := []struct {
		name    string
		config  BarkConfig
		wantErr bool
		server  string
	}{
		{name: "default server", config: BarkConfig{DeviceKey: "key-1"}, server: "https://api.day.app"},
		{name: "self hosted https", config: BarkConfig{Server: "https://bark.example.com/base/", DeviceKey: "key-1"}, server: "https://bark.example.com/base"},
		{name: "loopback http", config: BarkConfig{Server: "http://127.0.0.1:8080", DeviceKey: "key-1"}, server: "http://127.0.0.1:8080"},
		{name: "private http", config: BarkConfig{Server: "http://10.0.0.5:8080", DeviceKey: "key-1"}, server: "http://10.0.0.5:8080"},
		{name: "plain http public host", config: BarkConfig{Server: "http://bark.example.com", DeviceKey: "key-1"}, wantErr: true},
		{name: "embedded credentials", config: BarkConfig{Server: "https://user:pass@bark.example.com", DeviceKey: "key-1"}, wantErr: true},
		{name: "query string", config: BarkConfig{Server: "https://bark.example.com?key=x", DeviceKey: "key-1"}, wantErr: true},
		{name: "bad scheme", config: BarkConfig{Server: "ftp://bark.example.com", DeviceKey: "key-1"}, wantErr: true},
		{name: "bad device key", config: BarkConfig{DeviceKey: "bad key/with space"}, wantErr: true},
		{name: "bad level", config: BarkConfig{DeviceKey: "key-1", Level: "critical"}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			normalized, err := ValidateBark(test.config)
			if test.wantErr {
				if !errors.Is(err, ErrConfig) {
					t.Fatalf("err=%v want ErrConfig", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("validate: %v", err)
			}
			if normalized.Server != test.server {
				t.Fatalf("server=%q want %q", normalized.Server, test.server)
			}
			if normalized.Level != string(LevelActive) {
				t.Fatalf("level=%q want active", normalized.Level)
			}
		})
	}
}

func TestMessageClampsMultibyteText(t *testing.T) {
	message := Message{
		Title: strings.Repeat("通", MaxTitleRunes+20),
		Body:  strings.Repeat("知", MaxBodyBytes),
		Level: "unknown",
	}.Normalize()
	if utf8.RuneCountInString(message.Title) != MaxTitleRunes {
		t.Fatalf("title runes=%d want %d", utf8.RuneCountInString(message.Title), MaxTitleRunes)
	}
	if len(message.Body) > MaxBodyBytes {
		t.Fatalf("body bytes=%d want <= %d", len(message.Body), MaxBodyBytes)
	}
	// Clamping must never leave a torn UTF-8 sequence behind.
	for _, r := range message.Body {
		if r == utf8.RuneError {
			t.Fatalf("body contains an invalid rune")
		}
	}
	if message.Level != LevelActive {
		t.Fatalf("unknown level should fall back to active, got %q", message.Level)
	}
}

func TestSafeURLDropsCredentialsAndOddSchemes(t *testing.T) {
	if got := SafeURL("https://user:pass@example.com/x"); got != "" {
		t.Fatalf("credential URL accepted: %q", got)
	}
	if got := SafeURL("javascript:alert(1)"); got != "" {
		t.Fatalf("javascript URL accepted: %q", got)
	}
	if got := SafeURL("https://example.com/runs/1"); got != "https://example.com/runs/1" {
		t.Fatalf("valid URL rejected: %q", got)
	}
}
