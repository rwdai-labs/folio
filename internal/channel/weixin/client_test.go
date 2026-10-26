package weixin

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestProtocolHeadersAndTextReply(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ilink/bot/sendmessage" || r.Method != "POST" {
			t.Error("wrong operation")
		}
		for key, want := range map[string]string{"Authorization": "Bearer synthetic-token", "AuthorizationType": "ilink_bot_token", "iLink-App-Id": "bot", "iLink-App-ClientVersion": "132106", "Content-Type": "application/json"} {
			if r.Header.Get(key) != want {
				t.Errorf("%s header mismatch", key)
			}
		}
		uin, err := base64.StdEncoding.DecodeString(r.Header.Get("X-WECHAT-UIN"))
		if err != nil {
			t.Error(err)
		}
		if _, err := strconv.ParseUint(string(uin), 10, 32); err != nil {
			t.Error("invalid UIN")
		}
		var body struct {
			Msg struct {
				To     string `json:"to_user_id"`
				Client string `json:"client_id"`
				Type   int    `json:"message_type"`
				State  int    `json:"message_state"`
				Token  string `json:"context_token"`
				Items  []item `json:"item_list"`
			} `json:"msg"`
			Base map[string]string `json:"base_info"`
		}
		if json.NewDecoder(r.Body).Decode(&body) != nil {
			t.Error("invalid body")
		}
		if body.Msg.To != "peer" || body.Msg.Client != "client-stable" || body.Msg.Token != "synthetic-context" || body.Msg.Type != 2 || body.Msg.State != 2 || len(body.Msg.Items) != 1 || body.Msg.Items[0].Text.Text != "你好" || body.Base["bot_agent"] != "Folio/0.1.0" {
			t.Error("wrong reply envelope")
		}
		fmt.Fprint(w, `{"ret":0}`)
	}))
	defer server.Close()
	c := newClient(server.URL, "synthetic-token")
	if err := c.sendText(context.Background(), "peer", "synthetic-context", "client-stable", "你好"); err != nil {
		t.Fatal(err)
	}
}

func TestRequestRejectsErrorsRedirectsAndOversizedBodies(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		want       error
	}{
		{"expired-ret", `{"ret":-14,"errmsg":"synthetic-secret"}`, 200, ErrSessionExpired},
		{"expired-code", `{"errcode":-14}`, 200, ErrSessionExpired},
		{"business", `{"ret":7,"errmsg":"synthetic-secret"}`, 200, ErrUnavailable},
		{"http", `synthetic-secret`, 500, ErrUnavailable},
		{"auth", `synthetic-secret`, 401, ErrSessionExpired},
		{"malformed", `synthetic-secret`, 200, ErrProtocol},
		{"null", `null`, 200, ErrProtocol},
		{"oversized", strings.Repeat("x", maxResponseBytes+1), 200, ErrProtocol},
		{"redirect", ``, 302, ErrUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Location", "/must-not-follow?secret=synthetic-secret")
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			err := newClient(server.URL, "synthetic-token").request(context.Background(), "getupdates", map[string]any{}, true, time.Second, &struct{}{})
			if !errors.Is(err, tc.want) || strings.Contains(err.Error(), "synthetic-secret") {
				t.Fatalf("error not safely classified: %v", err)
			}
		})
	}
}

func TestEndpointAllowlistAndExactMessageIDs(t *testing.T) {
	for _, raw := range []string{"http://ilinkai.weixin.qq.com", "https://attacker.example", "https://ilinkai.weixin.qq.com.attacker.example", "https://user@ilinkai.weixin.qq.com", "https://ilinkai.weixin.qq.com:443", "https://ilinkai.weixin.qq.com/path", "https://ilinkai.weixin.qq.com?x=y"} {
		if validBaseURL(raw) {
			t.Errorf("accepted %s", raw)
		}
	}
	if !validBaseURL(DefaultBaseURL) || !validBaseURL("https://region.ilinkai.weixin.qq.com/") {
		t.Fatal("official endpoints rejected")
	}
	for _, raw := range []string{`"90071992547409931234"`, `90071992547409931234`} {
		var msg wireMessage
		if err := json.Unmarshal([]byte(`{"message_id":`+raw+`}`), &msg); err != nil || msg.ID != "90071992547409931234" {
			t.Fatal("message ID lost precision")
		}
	}
}

func TestTypingAndCancellation(t *testing.T) {
	var calls []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.Path)
		if strings.HasSuffix(r.URL.Path, "getconfig") {
			fmt.Fprint(w, `{"ret":0,"typing_ticket":"synthetic-ticket"}`)
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["status"] != float64(1) || body["typing_ticket"] != "synthetic-ticket" {
			t.Error("typing parameters")
		}
		fmt.Fprint(w, `{"ret":0}`)
	}))
	defer server.Close()
	c := newClient(server.URL, "synthetic-token")
	if err := c.Typing(context.Background(), "peer", "synthetic-context", true); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 2 {
		t.Fatal("typing did not use config ticket")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.getUpdates(ctx, "", time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation=%v", err)
	}
}
