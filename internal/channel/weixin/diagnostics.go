package weixin

import (
	"errors"
	"time"
)

// diagnostics is the bounded, redacted record of the last answered message and
// the last failure class. It never holds message text, peer identity or model
// output, so the console may display it beside the channel.
type diagnostics struct {
	Version     int    `json:"version"`
	LastOKAt    string `json:"last_ok_at,omitempty"`
	LastError   string `json:"last_error,omitempty"`
	LastErrorAt string `json:"last_error_at,omitempty"`
}

const (
	diagnosticsVersion = 1

	failureHandler  = "handler_failed"
	failureResult   = "result_invalid"
	failureRejected = "result_rejected"
	failureRunner   = "runner_failed"
	failureAuth     = "runner_auth"
	failureNetwork  = "runner_network"
	failureTimeout  = "runner_timeout"
	failureMissing  = "runner_unavailable"
	diagnosticsFile = "diagnostics.json"
)

// knownFailureClasses bounds what may be stored or shown. A corrupted or
// future value becomes "handler_failed" instead of reaching the page.
var knownFailureClasses = map[string]bool{
	failureHandler:  true,
	failureResult:   true,
	failureRejected: true,
	failureRunner:   true,
	failureAuth:     true,
	failureNetwork:  true,
	failureTimeout:  true,
	failureMissing:  true,
}

// loadDiagnostics reads the last outcomes, tolerating a missing or corrupt file:
// diagnostics never block the channel.
func loadDiagnostics(disk *Disk) diagnostics {
	if disk == nil {
		return diagnostics{}
	}
	var value diagnostics
	if err := disk.read(diagnosticsFile, &value); err != nil {
		return diagnostics{}
	}
	if value.Version != diagnosticsVersion || !knownFailureClasses[value.LastError] {
		return diagnostics{}
	}
	return value
}

// record stores one answered message. The write is best effort: a read-only
// volume must not turn a delivered reply into a failed one.
func (s *Service) record(class string) {
	now := time.Now().UTC().Format(time.RFC3339)
	value := s.diagnostics
	value.Version = diagnosticsVersion
	if class == "" {
		value.LastOKAt = now
	} else {
		value.LastError, value.LastErrorAt = class, now
	}
	s.diagnostics = value
	_ = s.disk.save(diagnosticsFile, value)
}

// failureClass extracts the fixed label a handler attached to its error.
func failureClass(err error) string {
	var coded interface{ FailureClass() string }
	if errors.As(err, &coded) {
		if class := coded.FailureClass(); knownFailureClasses[class] {
			return class
		}
	}
	return failureHandler
}

// failureReply turns a fixed class into owner-facing text. The text is generic
// by design: it never carries runner stderr, provider URLs or state.
func failureReply(class string) string {
	switch class {
	case failureAuth:
		return "Folio 的模型凭据已失效，暂时无法回答。请在服务端更新 Codex 登录后再试。"
	case failureNetwork:
		return "Folio 暂时连不上模型服务，请稍后重试。"
	case failureTimeout:
		return "Folio 处理超时，请稍后重新提问。"
	case failureMissing:
		return "Folio 的模型运行器尚未就绪，暂时无法回答。"
	case failureResult, failureRejected:
		return "Folio 的回复未通过校验，请换个问法再试。"
	}
	return "Folio 暂时无法处理这条消息，请稍后重试。"
}
