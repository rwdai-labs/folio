package weixin

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"folio/internal/channel"
)

type stubHandler struct{}

func (stubHandler) Authorize(channel.Message) error { return nil }
func (stubHandler) HandleMessage(context.Context, channel.Message) (string, error) {
	return "", nil
}

func stubFactory(string, string) (channel.Handler, error) { return stubHandler{}, nil }

// blockingRunner stands in for the poller so lifecycle tests never reach the
// network. It reports when the supervisor's cancellation actually arrived.
type blockingRunner struct{ stopped chan struct{} }

func (r *blockingRunner) Run(ctx context.Context) error {
	<-ctx.Done()
	if r.stopped != nil {
		select {
		case r.stopped <- struct{}{}:
		default:
		}
	}
	return ctx.Err()
}

// seed writes private state before the supervisor takes the directory lock.
func seed(t *testing.T, dir string, withCredentials bool, settings Settings) {
	t.Helper()
	disk, err := OpenDisk(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer disk.Close()
	if withCredentials {
		if err := disk.saveCredentials(Credentials{Token: "synthetic-token", AccountID: "bot", UserID: "owner", BaseURL: DefaultBaseURL}); err != nil {
			t.Fatal(err)
		}
	}
	if settings.Version != 0 {
		if err := disk.SaveSettings(settings); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSettingsRoundTripAndGuards(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	disk, err := OpenDisk(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer disk.Close()

	fresh, err := disk.Settings()
	if err != nil || fresh.Version != 1 || fresh.OwnerID != "" || fresh.Enabled {
		t.Fatalf("fresh settings = %+v, %v", fresh, err)
	}
	confirmed := Settings{Version: 1, OwnerID: "owner", Enabled: true}
	if err := disk.SaveSettings(confirmed); err != nil {
		t.Fatal(err)
	}
	reloaded, err := disk.Settings()
	if err != nil || reloaded != confirmed {
		t.Fatalf("reloaded = %+v, %v", reloaded, err)
	}
	// A persisted decision is private state: the directory is 0700 and the file
	// 0600, and the reader must enforce it rather than trust the writer.
	if info, err := os.Stat(filepath.Join(dir, settingsFile)); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("settings mode = %v, %v", info.Mode(), err)
	}
	if err := disk.SaveSettings(Settings{Version: 1, Enabled: true}); err == nil {
		t.Fatal("enabled channel persisted without an owner")
	}
	if err := disk.SaveSettings(Settings{Version: 1, OwnerID: strings.Repeat("a", 513)}); err == nil {
		t.Fatal("oversized owner accepted")
	}
	if err := disk.SaveSettings(Settings{Version: 1, OwnerID: "owner\nforged"}); err == nil {
		t.Fatal("control characters accepted in owner")
	}
}

func loginServer(t *testing.T, statuses []string) *httptest.Server {
	t.Helper()
	var poll int
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			fmt.Fprint(w, `{"qrcode":"synthetic-qr","qrcode_img_content":"synthetic-content"}`)
			return
		}
		if r.URL.Query().Get("qrcode") != "synthetic-qr" {
			t.Errorf("unexpected qrcode %q", r.URL.Query().Get("qrcode"))
		}
		value := statuses[len(statuses)-1]
		if poll < len(statuses) {
			value = statuses[poll]
		}
		poll++
		if value == "need_verifycode_check" {
			if r.URL.Query().Get("verify_code") != "123456" {
				fmt.Fprint(w, `{"status":"need_verifycode"}`)
				return
			}
			value = "confirmed"
		}
		if value == "confirmed" {
			fmt.Fprint(w, `{"status":"confirmed","bot_token":"synthetic-token","ilink_bot_id":"bot","ilink_user_id":"owner","baseurl":"https://ilinkai.weixin.qq.com"}`)
			return
		}
		fmt.Fprintf(w, `{"status":%q}`, value)
	}))
}

func waitFor(t *testing.T, what string, done func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if done() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestSessionRendersQRAndWaitsForConfirmation(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	disk, err := OpenDisk(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer disk.Close()

	server := loginServer(t, []string{"wait", "need_verifycode_check"})
	defer server.Close()

	session := startLoginWithClient(context.Background(), disk, newClient(server.URL, ""))
	defer session.Cancel()

	waitFor(t, "first QR", func() bool { return session.Snapshot().QRImage != "" })
	shot := session.Snapshot()
	if shot.Status != StatusWaiting || !strings.HasPrefix(shot.QRImage, "data:image/png;base64,") {
		t.Fatalf("waiting snapshot = %+v", shot)
	}
	if strings.Contains(shot.QRImage, "synthetic-content") {
		t.Fatal("raw QR payload leaked into the browser view")
	}
	if err := session.SubmitCode("123456"); !errors.Is(err, ErrNoPrompt) {
		t.Fatalf("code accepted before the phone asked for it: %v", err)
	}

	waitFor(t, "verification prompt", func() bool { return session.Snapshot().Status == StatusNeedCode })
	if err := session.SubmitCode("12ab"); !errors.Is(err, ErrProtocol) {
		t.Fatalf("non-digit code accepted: %v", err)
	}
	if err := session.SubmitCode("123456"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "owner confirmation", func() bool {
		_, ok := session.Awaiting()
		return ok
	})
	scanner, ok := session.Awaiting()
	if !ok || scanner != "owner" {
		t.Fatalf("scanner = %q, %v", scanner, ok)
	}
	if shot := session.Snapshot(); shot.ScannerID != "owner" || shot.QRImage != "" {
		t.Fatalf("confirmation snapshot = %+v", shot)
	}
	// A scan is not an owner decision: nothing may be enabled until Confirm.
	settings, err := disk.Settings()
	if err != nil || settings.PendingOwner != "owner" || settings.OwnerID != "" || settings.Enabled {
		t.Fatalf("post-login settings = %+v, %v", settings, err)
	}
}

func TestSessionReportsFailureCodesOnly(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	disk, err := OpenDisk(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer disk.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			fmt.Fprint(w, `{"qrcode":"synthetic-qr","qrcode_img_content":"synthetic-content"}`)
			return
		}
		fmt.Fprint(w, `{"status":"unexpected"}`)
	}))
	defer server.Close()
	session := startLoginWithClient(context.Background(), disk, newClient(server.URL, ""))
	waitFor(t, "failure", func() bool { return session.Snapshot().Status == StatusFailed })
	if code := session.Snapshot().Error; code != "protocol_error" {
		t.Fatalf("failure code = %q", code)
	}
}

func TestSupervisorDeploymentKillSwitch(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	sup := NewSupervisor(dir, ModeOff, "", stubFactory)
	if err := sup.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer sup.Close()
	if _, err := sup.StartLogin(); !errors.Is(err, ErrDeploymentOff) {
		t.Fatalf("login allowed under ModeOff: %v", err)
	}
	if _, err := sup.SetEnabled(true); !errors.Is(err, ErrDeploymentOff) {
		t.Fatalf("enable allowed under ModeOff: %v", err)
	}
	status := sup.Status()
	if status["state"] != "disabled" || status["enabled"] != false {
		t.Fatalf("status = %+v", status)
	}
}

func TestSupervisorRequiresOwnerAndLoginBeforePolling(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	seed(t, dir, false, Settings{Version: 1})
	sup := NewSupervisor(dir, ModeAuto, "", stubFactory)
	stopped := make(chan struct{})
	sup.newRunner = func(*Disk, channel.Handler) (runner, error) {
		return &blockingRunner{stopped: stopped}, nil
	}
	if err := sup.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer sup.Close()

	if _, err := sup.SetEnabled(true); !errors.Is(err, ErrOwnerRequired) {
		t.Fatalf("enable without owner: %v", err)
	}
	// A confirmed owner without a completed login still cannot poll.
	sup.mu.Lock()
	saveErr := sup.saveSettingsLocked(Settings{Version: 1, OwnerID: "owner"})
	sup.mu.Unlock()
	if saveErr != nil {
		t.Fatal(saveErr)
	}
	if _, err := sup.SetEnabled(true); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("enable without login: %v", err)
	}
}

func TestSupervisorEnableStartAndStop(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	seed(t, dir, true, Settings{Version: 1, OwnerID: "owner", Enabled: true})
	sup := NewSupervisor(dir, ModeAuto, "", stubFactory)
	stopped := make(chan struct{}, 4)
	sup.newRunner = func(*Disk, channel.Handler) (runner, error) {
		return &blockingRunner{stopped: stopped}, nil
	}
	if err := sup.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer sup.Close()
	waitFor(t, "configured poller", func() bool { return sup.Status()["running"] == true })

	value, err := sup.SetEnabled(false)
	if err != nil {
		t.Fatal(err)
	}
	if value["running"] != false {
		t.Fatalf("still running after disable: %+v", value)
	}
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("poller was not cancelled")
	}
	value, err = sup.SetEnabled(true)
	if err != nil {
		t.Fatal(err)
	}
	if value["running"] != true || value["enabled"] != true {
		t.Fatalf("re-enable = %+v", value)
	}
}

func TestSupervisorConfirmRejectsAnotherIdentity(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	seed(t, dir, true, Settings{Version: 1})
	sup := NewSupervisor(dir, ModeAuto, "", stubFactory)
	sup.newRunner = func(*Disk, channel.Handler) (runner, error) { return &blockingRunner{}, nil }
	if err := sup.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer sup.Close()

	// An interactive login is already holding the transport; enabling now would
	// race a second consumer of the same credentials.
	sup.mu.Lock()
	sup.session = &Session{status: StatusWaiting, done: make(chan struct{})}
	sup.mu.Unlock()
	if _, err := sup.SetEnabled(true); !errors.Is(err, ErrBusy) {
		t.Fatalf("enabled while a login was active: %v", err)
	}

	// The session goroutine writes the pending decision after the supervisor has
	// already read settings once; Confirm must re-read instead of trusting cache.
	if err := sup.disk.SaveSettings(Settings{Version: 1, PendingOwner: "owner"}); err != nil {
		t.Fatal(err)
	}
	sup.mu.Lock()
	sup.session = &Session{status: StatusAwaiting, scanner: "owner", done: make(chan struct{})}
	sup.mu.Unlock()

	if _, err := sup.Confirm("someone-else"); !errors.Is(err, ErrOwnerMismatch) {
		t.Fatalf("foreign identity confirmed: %v", err)
	}
	value, err := sup.Confirm("owner")
	if err != nil {
		t.Fatal(err)
	}
	if value["enabled"] != true || value["running"] != true || value["owner_id"] != "owner" {
		t.Fatalf("confirm = %+v", value)
	}
	settings, err := sup.disk.Settings()
	if err != nil || settings.OwnerID != "owner" || settings.PendingOwner != "" {
		t.Fatalf("persisted confirm = %+v, %v", settings, err)
	}
}

func TestSupervisorReportsLockedDirectoryAsBusy(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "private")
	holder, err := OpenDisk(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Close()
	sup := NewSupervisor(dir, ModeAuto, "", stubFactory)
	if err := sup.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer sup.Close()
	status := sup.Status()
	if status["state"] != "error" || status["error"] != "channel_busy" {
		t.Fatalf("locked status = %+v", status)
	}
	if _, err := sup.SetEnabled(true); err == nil {
		t.Fatal("enabled on a locked directory")
	}
}
