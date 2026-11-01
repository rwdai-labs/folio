package weixin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// seedContact writes a reply context before the supervisor takes the directory
// lock; the supervisor opens its own Disk afterwards.
func seedContact(t *testing.T, dir, user, token string) {
	t.Helper()
	disk, err := OpenDisk(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer disk.Close()
	if err := rememberContact(disk, user, token); err != nil {
		t.Fatal(err)
	}
}

func TestSendTextRequiresContext(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	seed(t, dir, true, Settings{Version: 1, OwnerID: "owner", Enabled: true})
	sup := NewSupervisor(dir, ModeAuto, "", stubFactory)
	defer sup.Close()
	// No peer has received a reply yet, so no context token exists.
	if err := sup.SendText(context.Background(), "hello"); !errors.Is(err, ErrNoContext) {
		t.Fatalf("missing context: %v", err)
	}
}

func TestSendTextUsesStoredContext(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	seed(t, dir, true, Settings{Version: 1, OwnerID: "owner", Enabled: true})
	seedContact(t, dir, "owner", "synthetic-context-owner")
	sup := NewSupervisor(dir, ModeAuto, "", stubFactory)
	defer sup.Close()

	var got struct {
		text     string
		token    string
		clientID string
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Msg struct {
				ClientID string `json:"client_id"`
				Token    string `json:"context_token"`
				To       string `json:"to_user_id"`
				Items    []item `json:"item_list"`
			} `json:"msg"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Error(err)
		}
		got.text = req.Msg.Items[0].Text.Text
		got.token = req.Msg.Token
		got.clientID = req.Msg.ClientID
		if req.Msg.To != "owner" {
			t.Errorf("peer = %q", req.Msg.To)
		}
		fmt.Fprint(w, `{"ret":0}`)
	}))
	defer server.Close()

	old := newPushClient
	newPushClient = func(creds Credentials) (*Client, error) { return newClient(server.URL, creds.Token), nil }
	defer func() { newPushClient = old }()

	if err := sup.SendText(context.Background(), "Folio finished"); err != nil {
		t.Fatal(err)
	}
	if got.text != "Folio finished" || got.token != "synthetic-context-owner" {
		t.Fatalf("push = %+v", got)
	}
	if !strings.HasPrefix(got.clientID, "folio-weixin-push-") {
		t.Fatalf("client id = %q", got.clientID)
	}
	if err := sup.SendText(context.Background(), "   "); !errors.Is(err, ErrProtocol) {
		t.Fatalf("blank text: %v", err)
	}
}

func TestSendTextRespectsDeploymentSwitch(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	seed(t, dir, true, Settings{Version: 1, OwnerID: "owner", Enabled: true})
	sup := NewSupervisor(dir, ModeOff, "", stubFactory)
	defer sup.Close()
	if err := sup.SendText(context.Background(), "hello"); !errors.Is(err, ErrDeploymentOff) {
		t.Fatalf("deployment switch: %v", err)
	}
}
