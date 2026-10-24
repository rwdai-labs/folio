package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// hookClient speaks the only contract Folio offers to an integration: a signed
// POST that turns an event into a durable task. It never retries a permanent
// rejection and never logs the secret or the signature.
type hookClient struct {
	endpoint string
	secret   []byte
	client   *http.Client
}

func newHookClient(config config) (*hookClient, error) {
	secret, err := readSecret(config.Folio.SecretFile)
	if err != nil {
		return nil, err
	}
	base, err := url.Parse(strings.TrimRight(config.Folio.URL, "/"))
	if err != nil {
		return nil, errors.New("folio.url is invalid")
	}
	endpoint := base.String() + "/api/hooks/" + config.Folio.HookID
	if parsed, parseErr := url.Parse(endpoint); parseErr != nil || parsed.Scheme == "" {
		return nil, errors.New("folio.url is invalid")
	}
	timeout := time.Duration(config.Folio.TimeoutSeconds) * time.Second
	return &hookClient{
		endpoint: endpoint,
		secret:   secret,
		client:   &http.Client{Timeout: timeout, CheckRedirect: refuseRedirect},
	}, nil
}

func refuseRedirect(_ *http.Request, _ []*http.Request) error {
	return http.ErrUseLastResponse
}

func readSecret(path string) ([]byte, error) {
	if raw := strings.TrimSpace(os.Getenv("FOLIO_HOOK_SECRET")); raw != "" {
		return validateSecret([]byte(raw))
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("hook secret is unavailable")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("hook secret must not be readable by group or others")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, errors.New("hook secret is unavailable")
	}
	return validateSecret(bytes.TrimSpace(raw))
}

func validateSecret(raw []byte) ([]byte, error) {
	if len(raw) < 16 || len(raw) > 256 {
		return nil, errors.New("hook secret has an invalid length")
	}
	for _, value := range raw {
		if value < 0x21 || value > 0x7e {
			return nil, errors.New("hook secret must be printable ASCII")
		}
	}
	return raw, nil
}

// send posts one event. A 2xx means Folio accepted the task (or already had the
// same event); anything else is an error, and the caller decides whether the
// event stays pending.
func (client *hookClient) send(ctx context.Context, eventID string, payload map[string]any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return permanentError("payload could not be encoded")
	}
	if len(body) > 16*1024 {
		return permanentError("payload is too large")
	}
	timestamp := time.Now().Unix()
	mac := hmac.New(sha256.New, client.secret)
	mac.Write([]byte(fmt.Sprintf("%d.%s", timestamp, body)))
	signature := hex.EncodeToString(mac.Sum(nil))

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, client.endpoint, bytes.NewReader(body))
	if err != nil {
		return permanentError("request could not be built")
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Folio-Timestamp", strconv.FormatInt(timestamp, 10))
	request.Header.Set("X-Folio-Event-Id", eventID)
	request.Header.Set("X-Folio-Signature", signature)

	response, err := client.client.Do(request)
	if err != nil {
		return errors.New("folio is unreachable")
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 8*1024))
	switch {
	case response.StatusCode == http.StatusAccepted, response.StatusCode == http.StatusOK:
		return nil
	case response.StatusCode == http.StatusTooManyRequests, response.StatusCode >= 500:
		return errors.New("folio is not ready")
	default:
		return permanentError(fmt.Sprintf("folio rejected the event with status %d", response.StatusCode))
	}
}

type permanentError string

func (value permanentError) Error() string { return string(value) }

func isPermanent(err error) bool {
	var target permanentError
	return errors.As(err, &target)
}
