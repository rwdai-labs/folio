package web

import (
	"errors"
	"net/http"
	"testing"
)

type modelServicesTestBackend struct {
	testBackend
	saved   int
	tested  int
	failure error
}

func (b *modelServicesTestBackend) ModelServices() (map[string]any, error) {
	return map[string]any{"version": 1, "connections": map[string]any{"gateway": map[string]any{"name": "Home gateway", "base_url": "https://models.example/v1", "key_present": true}}, "defaults": map[string]any{}, "overrides": map[string]any{}}, b.failure
}
func (b *modelServicesTestBackend) PutModelServices(payload map[string]any) (map[string]any, error) {
	b.saved++
	return b.ModelServices()
}
func (b *modelServicesTestBackend) TestModelService(payload map[string]any) (map[string]any, error) {
	b.tested++
	return map[string]any{"reachable": true, "inference_tested": false, "models": []any{map[string]any{"id": "group/report"}}}, b.failure
}

func TestModelServicesAuthorizationAndSafeErrors(t *testing.T) {
	backend := &modelServicesTestBackend{}
	server, password := testServer(t, backend)
	defer server.Close()
	client := &testClient{server: server}
	status, _, _ := client.request(t, http.MethodGet, "/api/model-services", nil, "")
	if status != http.StatusUnauthorized {
		t.Fatalf("unsigned status=%d", status)
	}
	status, _, _ = client.request(t, http.MethodPost, "/api/model-services", map[string]any{}, server.URL)
	if status != http.StatusUnauthorized {
		t.Fatalf("unsigned save status=%d", status)
	}
	client.login(t, password)
	status, _, value := client.request(t, http.MethodGet, "/api/model-services", nil, "")
	if status != http.StatusOK {
		t.Fatalf("get status=%d", status)
	}
	connection := value["connections"].(map[string]any)["gateway"].(map[string]any)
	if connection["key_present"] != true {
		t.Fatal("key presence lost to redaction")
	}
	savedCSRF := client.csrf
	client.csrf = ""
	status, _, _ = client.request(t, http.MethodPost, "/api/model-services", map[string]any{}, server.URL)
	if status != http.StatusForbidden || backend.saved != 0 {
		t.Fatalf("csrf status=%d calls=%d", status, backend.saved)
	}
	client.csrf = savedCSRF
	status, _, _ = client.request(t, http.MethodPost, "/api/model-services", map[string]any{"version": 1}, "https://untrusted.example")
	if status != http.StatusForbidden || backend.saved != 0 {
		t.Fatalf("origin status=%d calls=%d", status, backend.saved)
	}
	status, _, _ = client.request(t, http.MethodPost, "/api/model-services", map[string]any{"version": 1}, server.URL)
	if status != http.StatusOK || backend.saved != 1 {
		t.Fatalf("save status=%d calls=%d", status, backend.saved)
	}
	status, _, value = client.request(t, http.MethodPost, "/api/model-services/test", map[string]any{"connection": "gateway"}, server.URL)
	if status != http.StatusOK || value["inference_tested"] != false || backend.tested != 1 {
		t.Fatalf("test status=%d", status)
	}
	for _, tc := range []struct {
		err    string
		status int
		code   string
	}{
		{"model_services_invalid", 400, "model_services_invalid"},
		{"model_service_key_invalid", 400, "model_service_key_invalid"},
		{"model_service_unauthorized", 502, "model_service_unauthorized"},
		{"model_service_unreachable", 502, "model_service_unreachable"},
		{"model_service_invalid_response", 502, "model_service_invalid_response"},
		{"upstream Bearer synthetic-private-secret", 500, "model_services_unavailable"},
	} {
		backend.failure = errors.New(tc.err)
		status, _, value = client.request(t, http.MethodPost, "/api/model-services/test", map[string]any{}, server.URL)
		if status != tc.status || value["error"] != tc.code {
			t.Fatalf("error status=%d code=%v", status, value["error"])
		}
	}
}
