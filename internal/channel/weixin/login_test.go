package weixin

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestQRLoginVerificationAndCredentialRestore(t *testing.T) {
	disk := testDisk(t)
	shown, verified := false, false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("QR request leaked bot authorization")
		}
		if r.Method == "POST" {
			if r.URL.Query().Get("bot_type") != "3" || r.Header.Get("AuthorizationType") != "ilink_bot_token" {
				t.Error("QR headers or bot type")
			}
			var req map[string]any
			_ = json.NewDecoder(r.Body).Decode(&req)
			if _, ok := req["base_info"]; ok {
				t.Error("QR request contains bot metadata")
			}
			if len(req["local_token_list"].([]any)) != 1 {
				t.Error("saved token not offered for re-login")
			}
			fmt.Fprint(w, `{"qrcode":"synthetic-qr","qrcode_img_content":"https://example.invalid/synthetic-qr"}`)
			return
		}
		if r.Header.Get("AuthorizationType") != "" || r.Header.Get("X-WECHAT-UIN") != "" {
			t.Error("QR polling has bot auth headers")
		}
		if r.URL.Query().Get("qrcode") != "synthetic-qr" {
			t.Error("QR value changed")
		}
		if r.URL.Query().Get("verify_code") == "" {
			fmt.Fprint(w, `{"status":"need_verifycode"}`)
			return
		}
		if r.URL.Query().Get("verify_code") != "123456" {
			t.Error("verification value changed")
		}
		fmt.Fprint(w, `{"status":"confirmed","bot_token":"synthetic-new-token","ilink_bot_id":"bot","ilink_user_id":"owner","baseurl":"https://ilinkai.weixin.qq.com"}`)
	}))
	defer server.Close()
	creds, err := login(context.Background(), disk, newClient(server.URL, ""), LoginUI{
		ShowQR:           func(content string) error { shown = content == "https://example.invalid/synthetic-qr"; return nil },
		VerificationCode: func(context.Context) (string, error) { verified = true; return "123456", nil },
	})
	if err != nil || !shown || !verified || creds.Token != "synthetic-new-token" {
		t.Fatalf("QR login failed: %v", err)
	}
	restored, err := disk.Credentials()
	if err != nil || restored != creds {
		t.Fatal("credentials did not survive reload")
	}
}

func TestQRLoginRejectsUnsafeOrIncompleteConfirmation(t *testing.T) {
	for _, response := range []string{
		`{"status":"scaned_but_redirect","redirect_host":"attacker.example"}`,
		`{"status":"confirmed","bot_token":"synthetic-new-token","ilink_bot_id":"bot","ilink_user_id":"owner","baseurl":"https://attacker.example"}`,
		`{"status":"confirmed","ilink_bot_id":"bot"}`,
		`{"status":"unexpected"}`,
	} {
		t.Run(response, func(t *testing.T) {
			disk := testDisk(t)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "POST" {
					fmt.Fprint(w, `{"qrcode":"synthetic-qr","qrcode_img_content":"synthetic-content"}`)
					return
				}
				fmt.Fprint(w, response)
			}))
			defer server.Close()
			if _, err := login(context.Background(), disk, newClient(server.URL, ""), LoginUI{ShowQR: func(string) error { return nil }}); err == nil {
				t.Fatal("unsafe login succeeded")
			}
			creds, err := disk.Credentials()
			if err != nil || creds.Token != "synthetic-token" {
				t.Fatal("failed login replaced credentials")
			}
		})
	}
}
