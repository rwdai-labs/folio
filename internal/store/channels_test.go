package store

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestChannelStateStartsEmptyAndMasked(t *testing.T) {
	store := newTestStore(t)
	state, err := store.ChannelState()
	if err != nil {
		t.Fatalf("channel state: %v", err)
	}
	bark, ok := state["bark"].(map[string]any)
	if !ok {
		t.Fatalf("bark state=%v", state["bark"])
	}
	if bark["configured"] != false || bark["enabled"] != false {
		t.Fatalf("unexpected initial bark state: %v", bark)
	}
	if state["language"] != "en" {
		t.Fatalf("language=%v want en", state["language"])
	}
	if _, ok := state["deliveries"].([]any); !ok {
		t.Fatalf("deliveries=%v", state["deliveries"])
	}
}

func TestSaveBarkChannelMasksDeviceKey(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.SaveBarkChannel(map[string]any{"enabled": true}); err == nil {
		t.Fatal("saving an enabled channel without a device key should fail")
	}
	state, err := store.SaveBarkChannel(map[string]any{
		"enabled":    true,
		"server":     "https://api.day.app",
		"device_key": "device-key-alpha",
		"group":      "Folio",
		"level":      "timeSensitive",
		"language":   "zh",
	})
	if err != nil {
		t.Fatalf("save bark: %v", err)
	}
	bark := state["bark"].(map[string]any)
	if bark["configured"] != true || bark["enabled"] != true {
		t.Fatalf("unexpected bark state: %v", bark)
	}
	if state["language"] != "zh" {
		t.Fatalf("language=%v want zh", state["language"])
	}
	// The key must never leave the process, including inside nested payloads.
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "device-key-alpha") {
		t.Fatalf("device key leaked into the state payload: %s", raw)
	}
	if _, ok := bark["device_key"]; ok {
		t.Fatalf("device_key field must not be returned: %v", bark)
	}
	info, err := os.Stat(store.channelsPath)
	if err != nil {
		t.Fatalf("channel settings file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("channel settings mode=%o want 600", info.Mode().Perm())
	}
}

func TestSaveBarkChannelKeepsStoredKeyWhenBlank(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.SaveBarkChannel(map[string]any{
		"enabled":    true,
		"device_key": "device-key-alpha",
		"level":      "active",
	}); err != nil {
		t.Fatalf("save bark: %v", err)
	}
	// The UI submits the form without a key once one is stored.
	if _, err := store.SaveBarkChannel(map[string]any{
		"enabled": true,
		"group":   "Renamed",
		"level":   "active",
	}); err != nil {
		t.Fatalf("resave bark: %v", err)
	}
	settings, err := store.loadChannelSettings()
	if err != nil {
		t.Fatalf("load channels: %v", err)
	}
	if settings.Bark.DeviceKey != "device-key-alpha" {
		t.Fatalf("stored key changed: %q", settings.Bark.DeviceKey)
	}
	if settings.Bark.Group != "Renamed" {
		t.Fatalf("group=%q", settings.Bark.Group)
	}
}

func TestSaveBarkChannelRejectsPublicPlainHTTP(t *testing.T) {
	store := newTestStore(t)
	_, err := store.SaveBarkChannel(map[string]any{
		"enabled":    true,
		"server":     "http://bark.example.com",
		"device_key": "device-key-alpha",
	})
	if err == nil || err.Error() != "invalid_channel" {
		t.Fatalf("err=%v want invalid_channel", err)
	}
}

func TestTestBarkChannelRecordsDelivery(t *testing.T) {
	var delivered bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		delivered = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":200,"message":"success"}`))
	}))
	defer server.Close()

	store := newTestStore(t)
	if _, err := store.SaveBarkChannel(map[string]any{
		"enabled":    true,
		"server":     server.URL,
		"device_key": "device-key-alpha",
		"language":   "zh",
	}); err != nil {
		t.Fatalf("save bark: %v", err)
	}
	result, err := store.TestBarkChannel(nil)
	if err != nil {
		t.Fatalf("test bark: %v", err)
	}
	receipt, ok := result["receipt"].(map[string]any)
	if !ok || receipt["status"] != "delivered" {
		t.Fatalf("receipt=%v", result["receipt"])
	}
	if !delivered {
		t.Fatal("no request reached the bark server")
	}
	state := result["state"].(map[string]any)
	bark := state["bark"].(map[string]any)
	if bark["last_test_status"] != "delivered" {
		t.Fatalf("last_test_status=%v", bark["last_test_status"])
	}
	deliveries, ok := state["deliveries"].([]any)
	if !ok || len(deliveries) != 1 {
		t.Fatalf("deliveries=%v", state["deliveries"])
	}
	entry := deliveries[0].(map[string]any)
	if entry["kind"] != "test" || entry["channel"] != "bark" || entry["status"] != "delivered" {
		t.Fatalf("delivery entry=%v", entry)
	}
}

func TestTestBarkChannelRequiresConfiguration(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.TestBarkChannel(nil); err == nil || err.Error() != "channel_not_configured" {
		t.Fatalf("err=%v want channel_not_configured", err)
	}
}

func TestRunNotificationIsBoundedAndLocalized(t *testing.T) {
	run := map[string]any{
		"run_id":  "20261020T000000.000000000Z-abcdef12",
		"status":  "pushed",
		"summary": strings.Repeat("长", 2000),
		"edits":   []any{map[string]any{}, map[string]any{}},
	}
	zh := runNotification("zh", run)
	if zh.Title != "Folio · 上下文已更新" {
		t.Fatalf("zh title=%q", zh.Title)
	}
	if !strings.Contains(zh.Body, "已推送 2 处修改") {
		t.Fatalf("zh body=%q", zh.Body)
	}
	if len(zh.Body) > 3000 {
		t.Fatalf("zh body is not bounded: %d bytes", len(zh.Body))
	}
	en := runNotification("en", map[string]any{
		"run_id": "r1",
		"status": "failed",
		"stage":  "pull",
		"reason": "repository_sync_failed",
	})
	if en.Title != "Folio · Maintenance failed" || !strings.Contains(en.Body, "pull") {
		t.Fatalf("en message=%+v", en)
	}
}

func TestAllowNotificationThrottlesRepeatedKind(t *testing.T) {
	store := newTestStore(t)
	if !store.allowNotification("run:failed") {
		t.Fatal("first notification should be allowed")
	}
	if store.allowNotification("run:failed") {
		t.Fatal("second notification of the same kind should be throttled")
	}
	if !store.allowNotification("run:pushed") {
		t.Fatal("a different kind should not be throttled")
	}
}
