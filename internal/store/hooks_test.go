package store

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

func signHook(secret string, timestamp int64, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(strconv.FormatInt(timestamp, 10)))
	mac.Write([]byte("."))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

func createTestHook(t *testing.T, store *Store, name string) (string, string) {
	t.Helper()
	result, err := store.CreateHook(name, "", "")
	if err != nil {
		t.Fatalf("create hook: %v", err)
	}
	hook := result["hook"].(map[string]any)
	secret, _ := result["secret"].(string)
	if secret == "" {
		t.Fatal("create hook returned no secret")
	}
	return hook["id"].(string), secret
}

func TestCreateHookDerivesIDAndNeverListsSecret(t *testing.T) {
	store := newTestStore(t)
	id, secret := createTestHook(t, store, "Context repo updated")
	if id != "context-repo-updated" {
		t.Fatalf("id=%q", id)
	}
	if len(secret) < 32 {
		t.Fatalf("secret is too short: %q", secret)
	}
	listing, err := store.ListHooks()
	if err != nil {
		t.Fatalf("list hooks: %v", err)
	}
	raw, err := json.Marshal(listing)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), secret) {
		t.Fatalf("hook listing leaked the secret: %s", raw)
	}
	hooks := listing["hooks"].([]any)
	if len(hooks) != 1 {
		t.Fatalf("hooks=%v", hooks)
	}
	hook := hooks[0].(map[string]any)
	if hook["action"] != "maintenance" || hook["enabled"] != true {
		t.Fatalf("hook=%v", hook)
	}
	info, err := os.Stat(store.hooksPath)
	if err != nil {
		t.Fatalf("hooks file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("hooks file mode=%o want 600", info.Mode().Perm())
	}
}

func TestCreateHookGeneratesUniqueIDs(t *testing.T) {
	store := newTestStore(t)
	first, _ := createTestHook(t, store, "context")
	second, _ := createTestHook(t, store, "context")
	if first == second {
		t.Fatalf("duplicate hook id: %q", first)
	}
	if !strings.HasPrefix(second, "context-") {
		t.Fatalf("second id=%q", second)
	}
}

func TestCreateHookValidatesNameAndAction(t *testing.T) {
	store := newTestStore(t)
	if _, err := store.CreateHook("   ", "", ""); err == nil {
		t.Fatal("blank name should fail")
	}
	if _, err := store.CreateHook("x", "delete_everything", ""); err == nil || err.Error() != "invalid_hook_action" {
		t.Fatalf("err=%v want invalid_hook_action", err)
	}
	if _, err := store.CreateHook(strings.Repeat("n", 200), "", ""); err == nil {
		t.Fatal("overlong name should fail")
	}
}

func TestAcceptHookVerifiesSignatureAndReplay(t *testing.T) {
	store := newTestStore(t)
	id, secret := createTestHook(t, store, "watch")
	body := []byte(`{}`)
	now := time.Now().Unix()

	event, code := store.AcceptHook(id, now, "event-1", signHook(secret, now, body), body)
	if code != "ok" || event["action"] != "maintenance" || event["priority"] != "P2" {
		t.Fatalf("code=%q event=%v", code, event)
	}
	// The same event id must not start a second run.
	if _, code := store.AcceptHook(id, now, "event-1", signHook(secret, now, body), body); code != "duplicate_event" {
		t.Fatalf("replay code=%q", code)
	}
	// A modified body no longer matches the signature.
	if _, code := store.AcceptHook(id, now, "event-2", signHook(secret, now, body), []byte(`{"x":1}`)); code != "invalid_signature" {
		t.Fatalf("tampered body code=%q", code)
	}
	if _, code := store.AcceptHook(id, now, "event-3", "not-hex", body); code != "invalid_signature" {
		t.Fatalf("malformed signature code=%q", code)
	}
	if _, code := store.AcceptHook(id, now, "bad event id!", signHook(secret, now, body), body); code != "invalid_event_id" {
		t.Fatalf("event id code=%q", code)
	}
}

func TestAcceptHookRejectsStaleTimestamp(t *testing.T) {
	store := newTestStore(t)
	id, secret := createTestHook(t, store, "watch")
	body := []byte(`{}`)
	old := time.Now().Add(-10 * time.Minute).Unix()
	if _, code := store.AcceptHook(id, old, "event-old", signHook(secret, old, body), body); code != "stale_timestamp" {
		t.Fatalf("code=%q want stale_timestamp", code)
	}
	future := time.Now().Add(10 * time.Minute).Unix()
	if _, code := store.AcceptHook(id, future, "event-future", signHook(secret, future, body), body); code != "stale_timestamp" {
		t.Fatalf("future code=%q want stale_timestamp", code)
	}
}

func TestAcceptHookHonorsEnabledState(t *testing.T) {
	store := newTestStore(t)
	id, secret := createTestHook(t, store, "watch")
	if _, err := store.UpdateHook(id, false, "", ""); err != nil {
		t.Fatalf("disable: %v", err)
	}
	now := time.Now().Unix()
	body := []byte(`{}`)
	if _, code := store.AcceptHook(id, now, "event-1", signHook(secret, now, body), body); code != "hook_disabled" {
		t.Fatalf("code=%q want hook_disabled", code)
	}
	if _, code := store.AcceptHook("missing", now, "event-1", signHook(secret, now, body), body); code != "unknown_hook" {
		t.Fatalf("code=%q want unknown_hook", code)
	}
}

func TestRotateHookSecretInvalidatesOldSecret(t *testing.T) {
	store := newTestStore(t)
	id, oldSecret := createTestHook(t, store, "watch")
	result, err := store.RotateHookSecret(id)
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	newSecret, _ := result["secret"].(string)
	if newSecret == "" || newSecret == oldSecret {
		t.Fatalf("rotate returned secret=%q", newSecret)
	}
	now := time.Now().Unix()
	body := []byte(`{}`)
	if _, code := store.AcceptHook(id, now, "event-a", signHook(oldSecret, now, body), body); code != "invalid_signature" {
		t.Fatalf("old secret code=%q want invalid_signature", code)
	}
	if _, code := store.AcceptHook(id, now, "event-a", signHook(newSecret, now, body), body); code != "ok" {
		t.Fatalf("new secret code=%q", code)
	}
}

func TestAcceptHookRateLimitsRepeatedCalls(t *testing.T) {
	store := newTestStore(t)
	id, secret := createTestHook(t, store, "watch")
	now := time.Now().Unix()
	body := []byte(`{}`)
	for index := 0; index < hookRateLimit; index++ {
		eventID := "event-" + strconv.Itoa(index)
		if _, code := store.AcceptHook(id, now, eventID, signHook(secret, now, body), body); code != "ok" {
			t.Fatalf("call %d code=%q", index, code)
		}
	}
	if _, code := store.AcceptHook(id, now, "event-over", signHook(secret, now, body), body); code != "rate_limited" {
		t.Fatalf("code=%q want rate_limited", code)
	}
}

func TestDeleteHookRemovesIt(t *testing.T) {
	store := newTestStore(t)
	id, _ := createTestHook(t, store, "watch")
	listing, err := store.DeleteHook(id)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if hooks, _ := listing["hooks"].([]any); len(hooks) != 0 {
		t.Fatalf("hooks after delete=%v", hooks)
	}
	if _, err := store.DeleteHook(id); err == nil || err.Error() != "hook_not_found" {
		t.Fatalf("err=%v want hook_not_found", err)
	}
}
