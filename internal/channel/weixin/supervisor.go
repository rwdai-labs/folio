package weixin

import (
	"context"
	"crypto/subtle"
	"errors"
	"sync"
	"time"

	"folio/internal/channel"
)

// Mode is the deployment-level switch. The console may only change the
// channel state when the deployment leaves management to it.
type Mode string

const (
	// ModeAuto lets the authenticated console decide, starting from the
	// persisted decision. This is the default when the variable is unset.
	ModeAuto Mode = "auto"
	// ModeOn pins the channel on; a missing owner or credentials is a startup
	// error rather than a silent idle service.
	ModeOn Mode = "on"
	// ModeOff is a hard deployment kill switch: no login, no polling.
	ModeOff Mode = "off"
)

// Control errors map to stable client codes in the HTTP layer.
var (
	ErrDeploymentOff = errors.New("weixin: disabled by deployment")
	ErrBusy          = errors.New("weixin: another login is in progress")
	ErrNotConfigured = errors.New("weixin: login has not completed")
	ErrOwnerRequired = errors.New("weixin: an explicit owner is required")
	ErrOwnerMismatch = errors.New("weixin: confirmed owner does not match the scanner")
	ErrNoLogin       = errors.New("weixin: no login is awaiting confirmation")
	ErrNoPrompt      = errors.New("weixin: no verification prompt is active")
)

// HandlerFactory builds the Folio authorization handler for one account and
// owner pair. Store owns that decision; the transport merely asks for it.
type HandlerFactory func(accountID, ownerID string) (channel.Handler, error)

// runner is the poller the supervisor drives. It exists so tests can hold a
// session without reaching the network.
type runner interface {
	Run(context.Context) error
}

// Supervisor is the single in-process owner of the channel directory. Login and
// the poller share one Disk so the exclusive file lock never conflicts with
// itself, and every state change is serialized behind one mutex.
type Supervisor struct {
	dir       string
	mode      Mode
	pinned    string
	factory   HandlerFactory
	newRunner func(*Disk, channel.Handler) (runner, error)

	// control serializes operator mutations (login, confirm, enable, close).
	// It is always taken before mu so a running poller never observes a
	// half-applied decision.
	control  sync.Mutex
	mu       sync.Mutex
	ctx      context.Context
	disk     *Disk
	diskErr  error
	settings Settings

	runID   uint64
	running bool
	cancel  context.CancelFunc
	done    chan struct{}
	runCode string

	session *Session
	closed  bool
}

func NewSupervisor(dir string, mode Mode, pinned string, factory HandlerFactory) *Supervisor {
	if mode != ModeOn && mode != ModeOff {
		mode = ModeAuto
	}
	return &Supervisor{
		dir: dir, mode: mode, pinned: pinned, factory: factory,
		newRunner: func(disk *Disk, handler channel.Handler) (runner, error) {
			return NewService(disk, handler)
		},
	}
}

// Start applies the persisted decision. Under ModeOn a broken deployment is a
// startup error; under ModeAuto it is reported in status instead, so the console
// can explain what to fix.
func (s *Supervisor) Start(ctx context.Context) error {
	s.mu.Lock()
	s.ctx = ctx
	settings, err := s.settingsLocked()
	s.mu.Unlock()
	if err != nil {
		if s.mode == ModeOn {
			return err
		}
		return nil
	}
	if s.mode == ModeOff {
		return nil
	}
	enabled := s.mode == ModeOn || settings.Enabled
	if !enabled {
		return nil
	}
	if s.effectiveOwner(settings) == "" {
		if s.mode == ModeOn {
			return ErrOwnerRequired
		}
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.startLocked()
}

func (s *Supervisor) effectiveOwner(settings Settings) string {
	if s.pinned != "" {
		return s.pinned
	}
	return settings.OwnerID
}

// settingsLocked always reads the file: a completed login writes the pending
// owner from the session goroutine, so a process-wide cache would let Confirm
// compare against a stale decision. The file is tiny and bounded by the same
// rejecting reader as the credentials.
func (s *Supervisor) settingsLocked() (Settings, error) {
	disk, err := s.diskLocked()
	if err != nil {
		return Settings{}, err
	}
	settings, err := disk.Settings()
	if err != nil {
		return Settings{}, err
	}
	s.settings = settings
	return settings, nil
}

// diskLocked opens the private directory once and keeps it for the process
// lifetime. A second OpenDisk in the same process would fail the flock it
// already holds, so every caller shares this handle.
func (s *Supervisor) diskLocked() (*Disk, error) {
	if s.disk != nil {
		return s.disk, nil
	}
	if s.diskErr != nil {
		return nil, s.diskErr
	}
	disk, err := OpenDisk(s.dir)
	if err != nil {
		s.diskErr = err
		return nil, err
	}
	s.disk = disk
	return disk, nil
}

func (s *Supervisor) saveSettingsLocked(settings Settings) error {
	disk, err := s.diskLocked()
	if err != nil {
		return err
	}
	if err := disk.SaveSettings(settings); err != nil {
		return err
	}
	s.settings = settings
	return nil
}

// Status is the authenticated console view. It exposes no token, cursor or
// transport error, only fixed state and the confirmed scanner identity.
func (s *Supervisor) Status() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.statusLocked()
}

func (s *Supervisor) statusLocked() map[string]any {
	deployment := map[string]any{"mode": string(s.mode), "owner_pinned": s.pinned != ""}
	settings, settingsErr := s.settingsLocked()
	configured := false
	if settingsErr == nil {
		if disk := s.disk; disk != nil {
			configured = disk.HasCredentials()
		}
	}
	owner := s.effectiveOwner(settings)
	state := "idle"
	code := ""
	switch {
	case s.closed:
		state = "stopped"
	case s.mode == ModeOff:
		state, code = "disabled", "deployment_off"
	case s.diskErr != nil:
		state, code = "error", "channel_busy"
	case settingsErr != nil:
		state, code = "error", "state_error"
	case s.running:
		state = "running"
	case s.runCode != "":
		state, code = "error", s.runCode
	}
	value := map[string]any{
		"deployment": deployment,
		"configured": configured,
		"enabled":    s.mode == ModeOn || (settingsErr == nil && settings.Enabled),
		"running":    s.running,
		"owner_id":   owner,
		"state":      state,
		"login":      (*Snapshot)(nil),
	}
	if code != "" {
		value["error"] = code
	}
	if s.session != nil {
		value["login"] = s.session.Snapshot().Map()
	}
	return value
}

// StartLogin stops the poller, then drives one interactive attempt. The raw QR
// payload stays inside the process; only a rendered image reaches the browser.
func (s *Supervisor) StartLogin() (map[string]any, error) {
	s.control.Lock()
	defer s.control.Unlock()
	if s.mode == ModeOff {
		return nil, ErrDeploymentOff
	}
	s.mu.Lock()
	if s.session != nil {
		status := s.session.Snapshot().Status
		if status == StatusWaiting || status == StatusNeedCode || status == StatusVerifying {
			s.mu.Unlock()
			return nil, ErrBusy
		}
	}
	s.mu.Unlock()

	s.stop()

	s.mu.Lock()
	defer s.mu.Unlock()
	disk, err := s.diskLocked()
	if err != nil {
		return nil, err
	}
	if s.ctx == nil {
		s.ctx = context.Background()
	}
	s.session = StartLogin(s.ctx, disk)
	s.runCode = ""
	return s.statusLocked(), nil
}

func (s *Supervisor) LoginStatus() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.session == nil {
		return map[string]any{"login": (*Snapshot)(nil)}
	}
	return map[string]any{"login": s.session.Snapshot().Map()}
}

func (s *Supervisor) SubmitCode(code string) error {
	s.mu.Lock()
	session := s.session
	s.mu.Unlock()
	if session == nil {
		return ErrNoPrompt
	}
	return session.SubmitCode(code)
}

func (s *Supervisor) CancelLogin() {
	s.mu.Lock()
	session := s.session
	s.mu.Unlock()
	if session != nil {
		session.Cancel()
	}
}

// Confirm accepts the owner decision only when the submitted identity is the
// same scanner the transport just authenticated. The comparison is constant
// time and requires the same pending value persisted by the login.
func (s *Supervisor) Confirm(ownerID string) (map[string]any, error) {
	s.control.Lock()
	defer s.control.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.mode == ModeOff {
		return nil, ErrDeploymentOff
	}
	settings, err := s.settingsLocked()
	if err != nil {
		return nil, err
	}
	if s.session == nil {
		return nil, ErrNoLogin
	}
	scanner, ok := s.session.Awaiting()
	if !ok || scanner == "" || settings.PendingOwner == "" {
		return nil, ErrNoLogin
	}
	if subtle.ConstantTimeCompare([]byte(scanner), []byte(ownerID)) != 1 ||
		subtle.ConstantTimeCompare([]byte(scanner), []byte(settings.PendingOwner)) != 1 {
		return nil, ErrOwnerMismatch
	}
	next := Settings{Version: 1, OwnerID: scanner, Enabled: true}
	if err := s.saveSettingsLocked(next); err != nil {
		return nil, err
	}
	s.session = nil
	if err := s.startLocked(); err != nil {
		return nil, err
	}
	return s.statusLocked(), nil
}

// SetEnabled starts or stops polling from the console. Enabling requires a
// confirmed owner and a completed login; disabling is always allowed.
func (s *Supervisor) SetEnabled(enabled bool) (map[string]any, error) {
	s.control.Lock()
	defer s.control.Unlock()
	if s.mode == ModeOff {
		return nil, ErrDeploymentOff
	}
	if !enabled {
		s.stop()
		s.mu.Lock()
		defer s.mu.Unlock()
		settings, err := s.settingsLocked()
		if err != nil {
			return nil, err
		}
		settings.Enabled = false
		if err := s.saveSettingsLocked(settings); err != nil {
			return nil, err
		}
		return s.statusLocked(), nil
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	settings, err := s.settingsLocked()
	if err != nil {
		return nil, err
	}
	if s.session != nil {
		return nil, ErrBusy
	}
	if s.effectiveOwner(settings) == "" {
		return nil, ErrOwnerRequired
	}
	if s.disk == nil || !s.disk.HasCredentials() {
		return nil, ErrNotConfigured
	}
	settings.Enabled = true
	if err := s.saveSettingsLocked(settings); err != nil {
		return nil, err
	}
	if err := s.startLocked(); err != nil {
		settings.Enabled = false
		_ = s.saveSettingsLocked(settings)
		return nil, err
	}
	return s.statusLocked(), nil
}

func (s *Supervisor) startLocked() error {
	if s.running || s.session != nil {
		return nil
	}
	disk, err := s.diskLocked()
	if err != nil {
		return err
	}
	settings, err := s.settingsLocked()
	if err != nil {
		return err
	}
	owner := s.effectiveOwner(settings)
	if owner == "" {
		return ErrOwnerRequired
	}
	creds, err := disk.Credentials()
	if err != nil {
		return ErrNotConfigured
	}
	handler, err := s.factory(creds.AccountID, owner)
	if err != nil {
		return ErrOwnerRequired
	}
	service, err := s.newRunner(disk, handler)
	if err != nil {
		return err
	}
	if s.ctx == nil {
		s.ctx = context.Background()
	}
	ctx, cancel := context.WithCancel(s.ctx)
	s.runID++
	id := s.runID
	done := make(chan struct{})
	s.running, s.cancel, s.done, s.runCode = true, cancel, done, ""
	go func() {
		defer close(done)
		runErr := service.Run(ctx)
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.runID != id {
			return
		}
		s.running, s.cancel, s.done = false, nil, nil
		if runErr == nil || errors.Is(runErr, context.Canceled) || errors.Is(runErr, context.DeadlineExceeded) {
			return
		}
		switch {
		case errors.Is(runErr, ErrSessionExpired):
			s.runCode = "expired"
		case errors.Is(runErr, ErrState):
			s.runCode = "state_error"
		case errors.Is(runErr, ErrProtocol):
			s.runCode = "protocol_error"
		default:
			s.runCode = "channel_stopped"
		}
	}()
	return nil
}

// stop cancels a running poller and waits outside the mutex so the run
// goroutine can complete its own bookkeeping.
func (s *Supervisor) stop() {
	s.mu.Lock()
	cancel, done := s.cancel, s.done
	s.cancel, s.done, s.running = nil, nil, false
	s.mu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	if done == nil {
		return
	}
	select {
	case <-done:
	case <-time.After(10 * time.Second):
	}
}

func (s *Supervisor) Close() {
	s.control.Lock()
	defer s.control.Unlock()
	s.CancelLogin()
	s.stop()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	if s.disk != nil && !s.running {
		_ = s.disk.Close()
		s.disk = nil
	}
}
