package weixin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"folio/internal/channel"
)

// Only synthetic test handlers allow all peers. Production uses store's
// explicit account-and-owner policy before processing and reply delivery.
type testHandler func(context.Context, channel.Message) (string, error)

func (h testHandler) Authorize(channel.Message) error { return nil }

type deniedHandler struct{}

func (deniedHandler) Authorize(channel.Message) error { return channel.ErrUnauthorized }
func (deniedHandler) HandleMessage(context.Context, channel.Message) (string, error) {
	panic("denied message reached Folio")
}
func (h testHandler) HandleMessage(ctx context.Context, m channel.Message) (string, error) {
	return h(ctx, m)
}

func testDisk(t *testing.T) *Disk {
	t.Helper()
	d, err := OpenDisk(filepath.Join(t.TempDir(), "private"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Close() })
	if err := d.saveCredentials(Credentials{Token: "synthetic-token", AccountID: "bot", UserID: "owner", BaseURL: DefaultBaseURL}); err != nil {
		t.Fatal(err)
	}
	return d
}

func sampleMessage(id, sender string) wireMessage {
	return wireMessage{ID: wireID(id), From: sender, To: "bot", Type: 1, State: 2, ContextToken: "synthetic-context-" + sender, Items: []item{{Type: 1, Text: &textItem{Text: "你好"}}}}
}

func TestDurableInboxReplyRetryAndRestartDeduplication(t *testing.T) {
	disk := testDisk(t)
	var handled, sent, fetched atomic.Int32
	var clientID string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "getupdates") {
			fetched.Add(1)
			var req map[string]any
			_ = json.NewDecoder(r.Body).Decode(&req)
			if fetched.Load() > 1 && req["get_updates_buf"] != "cursor-next" {
				t.Error("cursor not restored")
			}
			// Backend replay and duplicate entries in a single batch.
			_ = json.NewEncoder(w).Encode(updates{Cursor: "cursor-next", Messages: []wireMessage{sampleMessage("1", "owner"), sampleMessage("1", "owner")}})
			return
		}
		var req struct {
			Msg struct {
				ClientID string `json:"client_id"`
				Token    string `json:"context_token"`
				To       string `json:"to_user_id"`
				Items    []item `json:"item_list"`
			} `json:"msg"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Msg.To != "owner" || req.Msg.Token != "synthetic-context-owner" || req.Msg.Items[0].Text.Text != "Folio reply" {
			t.Error("reply escaped its peer")
		}
		if sent.Add(1) == 1 {
			clientID = req.Msg.ClientID
			w.WriteHeader(503)
			return
		}
		if clientID != req.Msg.ClientID {
			t.Error("retry changed client ID")
		}
		fmt.Fprint(w, `{"ret":0}`)
	}))
	defer server.Close()
	handler := testHandler(func(_ context.Context, m channel.Message) (string, error) {
		handled.Add(1)
		var saved syncState
		if disk.read("sync.json", &saved) != nil || saved.Cursor != "cursor-next" || len(saved.Pending) != 1 {
			t.Error("handler ran before durable acceptance")
		}
		if m.SenderID != "owner" || m.ConversationID != "owner" || m.AccountID != "bot" {
			t.Error("wrong identity")
		}
		return "Folio reply", nil
	})
	s, err := NewService(disk, handler)
	if err != nil {
		t.Fatal(err)
	}
	s.client = newClient(server.URL, "synthetic-token")
	if err := s.step(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("send should fail: %v", err)
	}
	// Reconstruct from disk after send failure. Do not rerun the model.
	s, err = NewService(disk, handler)
	if err != nil {
		t.Fatal(err)
	}
	s.client = newClient(server.URL, "synthetic-token")
	if err := s.step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if handled.Load() != 1 || sent.Load() != 2 || fetched.Load() != 2 || len(s.state.Pending) != 0 {
		t.Fatalf("duplicate handling: calls=%d sends=%d polls=%d", handled.Load(), sent.Load(), fetched.Load())
	}
}

func TestIdentityIsolationUnsupportedMessagesAndUnauthorizedPeer(t *testing.T) {
	a := sampleMessage("same-id", "alice")
	b := sampleMessage("same-id", "bob")
	a.Session, b.Session = "same-session", "same-session"
	ma, _ := incoming("bot", a)
	mb, _ := incoming("bot", b)
	if ma.Key() == mb.Key() || ma.SessionKey() == mb.SessionKey() {
		t.Fatal("users share dedup or sessions")
	}
	other := ma
	other.AccountID = "another-bot"
	if ma.Key() == other.Key() || ma.SessionKey() == other.SessionKey() {
		t.Fatal("accounts share sessions")
	}
	for _, mutate := range []func(*wireMessage){
		func(m *wireMessage) { m.Group = "group" },
		func(m *wireMessage) { m.Type = 2 },
		func(m *wireMessage) { m.State = 1 },
		func(m *wireMessage) { m.From = "" },
		func(m *wireMessage) { m.ID = "" },
		func(m *wireMessage) { m.ContextToken = "" },
		func(m *wireMessage) { m.To = "another-bot" },
		func(m *wireMessage) { m.Items = []item{{Type: 3}} },
		func(m *wireMessage) { m.Items[0].Text = &textItem{Text: strings.Repeat("x", maxTextBytes+1)} },
	} {
		m := sampleMessage("1", "alice")
		mutate(&m)
		if _, ok := incoming("bot", m); ok {
			t.Fatal("unsupported envelope accepted")
		}
	}
	disk := testDisk(t)
	state := &syncState{Version: 1, AccountID: "bot", Cursor: "accepted", Pending: []delivery{{Message: mb, ContextToken: "synthetic-context"}}}
	if err := disk.save("sync.json", state); err != nil {
		t.Fatal(err)
	}
	s, err := NewService(disk, testHandler(func(context.Context, channel.Message) (string, error) { return "", channel.ErrUnauthorized }))
	if err != nil {
		t.Fatal(err)
	}
	// No network server: an unauthorized peer must never cause a send.
	if err := s.drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(s.state.Pending) != 0 || len(s.state.Recent) != 1 {
		t.Fatal("denied message not acknowledged")
	}
}

func TestCorruptMismatchedOrUnsafeStateFailsClosed(t *testing.T) {
	disk := testDisk(t)
	if _, err := OpenDisk(disk.dir); err == nil {
		t.Fatal("second poller acquired same state")
	}
	for _, body := range []string{`{`, `{"version":1,"account_id":"another-bot"}`, `{"version":2,"account_id":"bot"}`} {
		if os.WriteFile(filepath.Join(disk.dir, "sync.json"), []byte(body), 0o600) != nil {
			t.Fatal("write fixture")
		}
		if _, err := disk.state("bot"); !errors.Is(err, ErrState) {
			t.Fatal("state reset instead of failing closed")
		}
	}
	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(filepath.Join(disk.dir, "sync.json"))
	if os.Symlink(outside, filepath.Join(disk.dir, "sync.json")) != nil {
		t.Fatal("symlink fixture")
	}
	if disk.save("sync.json", &syncState{}) == nil {
		t.Fatal("symlink state accepted")
	}
	if raw, _ := os.ReadFile(outside); string(raw) != "untouched" {
		t.Fatal("external file modified")
	}
	if _, err := disk.state("bot"); err == nil {
		t.Fatal("symlink read accepted")
	}
	if info, _ := os.Stat(filepath.Join(disk.dir, "credentials.json")); info.Mode().Perm() != 0o600 {
		t.Fatal("credentials not private")
	}
	if info, _ := os.Stat(disk.dir); info.Mode().Perm() != 0o700 {
		t.Fatal("directory not private")
	}
	if os.Chmod(filepath.Join(disk.dir, "credentials.json"), 0o644) != nil {
		t.Fatal("chmod fixture")
	}
	if _, err := disk.Credentials(); err == nil {
		t.Fatal("world-readable credentials accepted")
	}
}

func TestStateRefusesSymlinkDirectoriesAndGitWorktrees(t *testing.T) {
	root := t.TempDir()
	if os.Mkdir(filepath.Join(root, ".git"), 0o700) != nil {
		t.Fatal("git marker fixture")
	}
	if _, err := OpenDisk(filepath.Join(root, "private", "weixin")); err == nil {
		t.Fatal("state allowed in Git worktree")
	}
	if _, err := os.Stat(filepath.Join(root, "private")); !os.IsNotExist(err) {
		t.Fatal("refused path created files")
	}
	link := filepath.Join(t.TempDir(), "link")
	if os.Symlink(t.TempDir(), link) != nil {
		t.Fatal("symlink fixture")
	}
	if _, err := OpenDisk(filepath.Join(link, "weixin")); err == nil {
		t.Fatal("symlink directory accepted")
	}
}

func TestRevocationDiscardsPreparedReplyWithoutDelivery(t *testing.T) {
	disk := testDisk(t)
	m, _ := incoming("bot", sampleMessage("1", "former-owner"))
	state := &syncState{Version: 1, AccountID: "bot", Pending: []delivery{{Message: m, ContextToken: "synthetic-context", Ready: true, Reply: "Former owner's private answer"}}}
	if disk.save("sync.json", state) != nil {
		t.Fatal("state fixture")
	}
	s, err := NewService(disk, deniedHandler{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.drain(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(s.state.Pending) != 0 || len(s.state.Recent) != 1 {
		t.Fatal("revoked reply retained")
	}
}

func TestCancellationKeepsAcceptedWork(t *testing.T) {
	disk := testDisk(t)
	m, _ := incoming("bot", sampleMessage("1", "owner"))
	state := &syncState{Version: 1, AccountID: "bot", Pending: []delivery{{Message: m, ContextToken: "synthetic-context"}}}
	if disk.save("sync.json", state) != nil {
		t.Fatal("state fixture")
	}
	ctx, cancel := context.WithCancel(context.Background())
	s, _ := NewService(disk, testHandler(func(context.Context, channel.Message) (string, error) { cancel(); return "", context.Canceled }))
	if err := s.drain(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation lost")
	}
	restored, err := disk.state("bot")
	if err != nil || len(restored.Pending) != 1 || restored.Pending[0].Ready {
		t.Fatal("canceled message acknowledged")
	}
}
