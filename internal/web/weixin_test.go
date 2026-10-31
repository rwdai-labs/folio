package web

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"folio/internal/channel/weixin"
)

// testWeixin records what the HTTP layer asked for, so routing assertions do
// not need a real supervisor or a real transport.
type testWeixin struct {
	status     map[string]any
	startErr   error
	codeErr    error
	confirmErr error
	enableErr  error
	codes      []string
	confirmed  []string
	enabled    []bool
	started    int
	canceled   int
}

func (w *testWeixin) WeixinStatus() map[string]any { return w.status }

func (w *testWeixin) WeixinStartLogin() (map[string]any, error) {
	w.started++
	if w.startErr != nil {
		return nil, w.startErr
	}
	return map[string]any{"login": map[string]any{"status": "waiting", "qr_image": "data:image/png;base64,c3ludGhldGlj"}}, nil
}

func (w *testWeixin) WeixinLoginStatus() map[string]any {
	return map[string]any{"login": map[string]any{"status": "waiting"}}
}

func (w *testWeixin) WeixinSubmitCode(code string) error {
	if w.codeErr != nil {
		return w.codeErr
	}
	w.codes = append(w.codes, code)
	return nil
}

func (w *testWeixin) WeixinCancelLogin() { w.canceled++ }

func (w *testWeixin) WeixinConfirm(ownerID string) (map[string]any, error) {
	if w.confirmErr != nil {
		return nil, w.confirmErr
	}
	w.confirmed = append(w.confirmed, ownerID)
	return map[string]any{"enabled": true, "running": true, "owner_id": ownerID}, nil
}

func (w *testWeixin) WeixinSetEnabled(enabled bool) (map[string]any, error) {
	if w.enableErr != nil {
		return nil, w.enableErr
	}
	w.enabled = append(w.enabled, enabled)
	return map[string]any{"enabled": enabled}, nil
}

func weixinStatusFixture() map[string]any {
	return map[string]any{
		"deployment": map[string]any{"mode": "auto", "owner_pinned": false},
		"configured": true,
		"enabled":    false,
		"running":    false,
		"owner_id":   "",
		"state":      "idle",
		"login":      nil,
		// A backend must not be able to push a credential through this route
		// even if it puts one in the map.
		"token": "synthetic-token",
	}
}

func TestWeixinRoutesRequireSession(t *testing.T) {
	backend := &testBackend{}
	weixinFake := &testWeixin{status: weixinStatusFixture()}
	server, password := testServerWith(t, backend, Options{Weixin: weixinFake})
	defer server.Close()
	client := &testClient{server: server}

	status, _, _ := client.request(t, http.MethodGet, "/api/weixin", nil, "")
	if status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status route = %d", status)
	}
	status, _, _ = client.request(t, http.MethodGet, "/api/weixin/login", nil, "")
	if status != http.StatusUnauthorized {
		t.Fatalf("unauthenticated login poll = %d", status)
	}

	client.login(t, password)
	status, _, result := client.request(t, http.MethodGet, "/api/weixin", nil, "")
	if status != http.StatusOK {
		t.Fatalf("status route = %d %v", status, result)
	}
	if result["token"] != "[redacted]" {
		t.Fatalf("credential-shaped field survived redaction: %v", result["token"])
	}
	if result["state"] != "idle" || result["configured"] != true {
		t.Fatalf("status payload = %v", result)
	}
	if weixinFake.started != 0 {
		t.Fatalf("a GET started a login")
	}
}

func TestWeixinMutationRequiresCSRFAndOrigin(t *testing.T) {
	backend := &testBackend{}
	weixinFake := &testWeixin{status: weixinStatusFixture()}
	server, password := testServerWith(t, backend, Options{Weixin: weixinFake})
	defer server.Close()
	client := &testClient{server: server}
	client.login(t, password)

	csrf := client.csrf
	client.csrf = ""
	status, _, result := client.request(t, http.MethodPost, "/api/weixin/login", map[string]any{}, server.URL)
	if status != http.StatusForbidden || result["error"] != "csrf_failed" {
		t.Fatalf("missing CSRF = %d %v", status, result)
	}
	client.csrf = csrf

	status, _, result = client.request(t, http.MethodPost, "/api/weixin/enabled", map[string]any{"enabled": true}, "http://localhost:1")
	if status != http.StatusForbidden || result["error"] != "origin_forbidden" {
		t.Fatalf("foreign origin = %d %v", status, result)
	}
	if weixinFake.started != 0 || len(weixinFake.enabled) != 0 {
		t.Fatal("refused request reached the channel")
	}
}

func TestWeixinLoginFlowAndStableErrorCodes(t *testing.T) {
	backend := &testBackend{}
	weixinFake := &testWeixin{status: weixinStatusFixture()}
	server, password := testServerWith(t, backend, Options{Weixin: weixinFake})
	defer server.Close()
	client := &testClient{server: server}
	client.login(t, password)

	status, _, result := client.request(t, http.MethodPost, "/api/weixin/login", map[string]any{}, server.URL)
	if status != http.StatusAccepted {
		t.Fatalf("login start = %d %v", status, result)
	}
	login, ok := result["login"].(map[string]any)
	if !ok || login["status"] != "waiting" {
		t.Fatalf("login payload = %v", result)
	}

	if status, _, _ = client.request(t, http.MethodPost, "/api/weixin/login/code", map[string]any{"code": "123456"}, server.URL); status != http.StatusOK {
		t.Fatalf("code submit = %d", status)
	}
	if len(weixinFake.codes) != 1 || weixinFake.codes[0] != "123456" {
		t.Fatalf("code not forwarded: %v", weixinFake.codes)
	}

	status, _, result = client.request(t, http.MethodPost, "/api/weixin/confirm", map[string]any{"owner_id": "owner"}, server.URL)
	if status != http.StatusOK || result["enabled"] != true {
		t.Fatalf("confirm = %d %v", status, result)
	}
	if len(weixinFake.confirmed) != 1 || weixinFake.confirmed[0] != "owner" {
		t.Fatalf("owner not forwarded: %v", weixinFake.confirmed)
	}

	status, _, _ = client.request(t, http.MethodPost, "/api/weixin/enabled", map[string]any{"enabled": false}, server.URL)
	if status != http.StatusOK || len(weixinFake.enabled) != 1 || weixinFake.enabled[0] {
		t.Fatalf("disable = %d %v", status, weixinFake.enabled)
	}

	// Malformed payloads are rejected before the channel sees them.
	for _, probe := range []struct {
		path    string
		payload map[string]any
		code    string
	}{
		{"/api/weixin/login/code", map[string]any{"code": 12}, "weixin_invalid_code"},
		{"/api/weixin/login/code", map[string]any{"code": strings.Repeat("9", 33)}, "weixin_invalid_code"},
		{"/api/weixin/confirm", map[string]any{"owner_id": ""}, "weixin_invalid_owner"},
		{"/api/weixin/enabled", map[string]any{"enabled": "yes"}, "weixin_invalid_request"},
	} {
		status, _, result = client.request(t, http.MethodPost, probe.path, probe.payload, server.URL)
		if status != http.StatusBadRequest || result["error"] != probe.code {
			t.Fatalf("%s %v = %d %v", probe.path, probe.payload, status, result)
		}
	}
}

func TestWeixinErrorMapping(t *testing.T) {
	for _, probe := range []struct {
		err    error
		status int
		code   string
	}{
		{weixin.ErrBusy, http.StatusConflict, "weixin_busy"},
		{weixin.ErrDeploymentOff, http.StatusForbidden, "weixin_deployment_off"},
		{weixin.ErrOwnerMismatch, http.StatusConflict, "weixin_owner_mismatch"},
		{weixin.ErrNoLogin, http.StatusConflict, "weixin_no_login"},
		{weixin.ErrNoPrompt, http.StatusConflict, "weixin_no_prompt"},
		{weixin.ErrState, http.StatusInternalServerError, "weixin_state_error"},
		{errors.New("weixin: raw detail that must not surface"), http.StatusInternalServerError, "weixin_failed"},
	} {
		backend := &testBackend{}
		failing := &testWeixin{status: weixinStatusFixture(), startErr: probe.err}
		server, password := testServerWith(t, backend, Options{Weixin: failing})
		client := &testClient{server: server}
		client.login(t, password)
		status, _, result := client.request(t, http.MethodPost, "/api/weixin/login", map[string]any{}, server.URL)
		server.Close()
		if status != probe.status || result["error"] != probe.code {
			t.Fatalf("%v = %d %v, want %d %s", probe.err, status, result, probe.status, probe.code)
		}
		encoded := result["error"]
		if strings.Contains(strings.ToLower(encoded.(string)), "raw detail") {
			t.Fatalf("raw error text reached the page: %v", encoded)
		}
	}
}

func TestWeixinUnavailableWithoutSupervisor(t *testing.T) {
	backend := &testBackend{}
	server, password := testServerWith(t, backend, Options{})
	defer server.Close()
	client := &testClient{server: server}
	client.login(t, password)
	for _, probe := range []struct{ method, path string }{
		{http.MethodGet, "/api/weixin"},
		{http.MethodGet, "/api/weixin/login"},
		{http.MethodPost, "/api/weixin/login"},
		{http.MethodPost, "/api/weixin/enabled"},
	} {
		status, _, result := client.request(t, probe.method, probe.path, map[string]any{"enabled": true}, server.URL)
		if status != http.StatusServiceUnavailable || result["error"] != "weixin_unavailable" {
			t.Fatalf("%s %s = %d %v", probe.method, probe.path, status, result)
		}
	}
}
