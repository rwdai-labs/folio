package weixin

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"sync"
	"time"

	qrcode "github.com/skip2/go-qrcode"
)

// Login status values cross the authenticated HTTP boundary. They are fixed
// strings so a new transport state cannot leak upstream wording into the page.
const (
	StatusWaiting   = "waiting"
	StatusNeedCode  = "need_code"
	StatusVerifying = "verifying"
	StatusAwaiting  = "awaiting_confirmation"
	StatusExpired   = "expired"
	StatusFailed    = "failed"
	StatusCanceled  = "canceled"
)

// loginTTL mirrors the transport's own five-minute QR deadline.
const loginTTL = 5 * time.Minute

// qrPixelSize keeps the rendered image readable on a phone-sized viewport
// without shipping the raw QR payload to the browser.
const qrPixelSize = 320

// Session drives one interactive login for the authenticated console. The
// transport callbacks are supplied here instead of a terminal, but the raw QR
// payload never leaves the process: only a rendered PNG data URL is exposed.
type Session struct {
	disk *Disk

	mu      sync.Mutex
	status  string
	qr      string
	scanner string
	code    string
	started time.Time
	expires time.Time

	codes  chan string
	cancel context.CancelFunc
	done   chan struct{}
}

// StartLogin begins one login attempt. It returns immediately; callers poll
// Snapshot and must not start a second attempt while the first is active.
func StartLogin(parent context.Context, disk *Disk) *Session {
	return startLoginWithClient(parent, disk, newClient(DefaultBaseURL, ""))
}

// startLoginWithClient keeps the interactive state machine testable against a
// local fake transport; production always uses the fixed official base URL.
func startLoginWithClient(parent context.Context, disk *Disk, client *Client) *Session {
	ctx, cancel := context.WithCancel(parent)
	now := time.Now()
	s := &Session{
		disk:    disk,
		status:  StatusWaiting,
		started: now,
		expires: now.Add(loginTTL),
		codes:   make(chan string, 1),
		cancel:  cancel,
		done:    make(chan struct{}),
	}
	go s.run(ctx, client)
	return s
}

func (s *Session) run(ctx context.Context, client *Client) {
	defer close(s.done)
	_, err := login(ctx, s.disk, client, LoginUI{
		ShowQR: func(content string) error {
			code, err := qrcode.New(content, qrcode.Medium)
			if err != nil {
				return ErrProtocol
			}
			image, err := code.PNG(qrPixelSize)
			if err != nil {
				return ErrProtocol
			}
			s.setQR("data:image/png;base64," + base64.StdEncoding.EncodeToString(image))
			return nil
		},
		VerificationCode: func(wait context.Context) (string, error) {
			s.setStatus(StatusNeedCode)
			select {
			case value := <-s.codes:
				s.setStatus(StatusVerifying)
				return value, nil
			case <-wait.Done():
				return "", wait.Err()
			}
		},
	})
	s.finish(err)
}

func (s *Session) setQR(image string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.qr = image
	s.status = StatusWaiting
}

func (s *Session) setStatus(status string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status != StatusAwaiting {
		s.status = status
	}
}

// finish records a completed attempt. Credentials are already persisted by the
// transport; the pending owner still needs an explicit confirmation from the
// authenticated administrator before the channel may poll.
func (s *Session) finish(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.qr = ""
	if err == nil {
		creds, credErr := s.disk.Credentials()
		if credErr != nil || !validOwner(creds.UserID) || creds.UserID == "" {
			s.status, s.code = StatusFailed, "state_error"
			return
		}
		// Every fresh login requires confirmation again, even for the same
		// scanner: a scan is not the same event as the owner's decision.
		settings := Settings{Version: 1, PendingOwner: creds.UserID}
		if saveErr := s.disk.SaveSettings(settings); saveErr != nil {
			s.status, s.code = StatusFailed, "state_error"
			return
		}
		s.scanner = creds.UserID
		s.status = StatusAwaiting
		return
	}
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		s.status = StatusCanceled
		if errors.Is(err, context.DeadlineExceeded) {
			s.status = StatusExpired
		}
	case errors.Is(err, ErrSessionExpired):
		s.status, s.code = StatusExpired, "expired"
	case errors.Is(err, ErrProtocol):
		s.status, s.code = StatusFailed, "protocol_error"
	case errors.Is(err, ErrState), errors.Is(err, os.ErrPermission):
		s.status, s.code = StatusFailed, "state_error"
	case errors.Is(err, ErrUnavailable):
		s.status, s.code = StatusFailed, "unavailable"
	default:
		s.status, s.code = StatusFailed, "login_failed"
	}
}

// Snapshot is the only shape the HTTP layer may serialize. It never includes
// the token, the raw QR payload or a raw transport error.
type Snapshot struct {
	Status    string `json:"status"`
	QRImage   string `json:"qr_image,omitempty"`
	ScannerID string `json:"scanner_id,omitempty"`
	Error     string `json:"error,omitempty"`
	ExpiresAt string `json:"expires_at,omitempty"`
}

// Map is the serialization shape. The Web layer redacts by field name and only
// recurses into maps, so a struct would be flattened into one string and would
// smuggle the rendered QR past the field rules.
func (value Snapshot) Map() map[string]any {
	return map[string]any{
		"status":     value.Status,
		"qr_image":   value.QRImage,
		"scanner_id": value.ScannerID,
		"error":      value.Error,
		"expires_at": value.ExpiresAt,
	}
}

func (s *Session) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	value := Snapshot{Status: s.status, Error: s.code}
	if s.status == StatusWaiting || s.status == StatusNeedCode || s.status == StatusVerifying {
		value.QRImage = s.qr
		value.ExpiresAt = s.expires.UTC().Format(time.RFC3339)
	}
	if s.status == StatusAwaiting {
		value.ScannerID = s.scanner
	}
	return value
}

// SubmitCode delivers the digits the phone displayed. It is only valid while
// the transport is waiting for verification; anything else is a stale form.
func (s *Session) SubmitCode(code string) error {
	if len(code) < 1 || len(code) > 12 {
		return ErrProtocol
	}
	for _, r := range code {
		if r < '0' || r > '9' {
			return ErrProtocol
		}
	}
	s.mu.Lock()
	waiting := s.status == StatusNeedCode
	s.mu.Unlock()
	if !waiting {
		return ErrNoPrompt
	}
	select {
	case s.codes <- code:
		return nil
	default:
		return ErrNoPrompt
	}
}

func (s *Session) Cancel() {
	s.cancel()
}

func (s *Session) Done() <-chan struct{} { return s.done }

// Awaiting reports whether the attempt finished and still needs an owner
// decision. It is used to keep the confirmation form available across reloads
// without re-exposing a consumed QR.
func (s *Session) Awaiting() (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status != StatusAwaiting {
		return "", false
	}
	return s.scanner, true
}
