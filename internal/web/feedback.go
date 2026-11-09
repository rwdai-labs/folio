package web

import (
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

const (
	maxFeedbackUploadBytes = 8 * 1024 * 1024
	// The image itself stays capped at maxFeedbackUploadBytes. The multipart
	// envelope adds field headers and boundaries, so the total request gets a
	// separate bounded allowance instead of rejecting a legal maximum image.
	maxFeedbackMultipartBytes = maxFeedbackUploadBytes + 1024*1024
)

var safeFeedbackID = regexp.MustCompile(`^f-[0-9a-f]{12}$`)

func isMultipartRequest(r *http.Request) bool {
	contentType := strings.ToLower(strings.TrimSpace(strings.Split(r.Header.Get("Content-Type"), ";")[0]))
	return contentType == "multipart/form-data"
}

// createFeedback accepts only the bounded multipart contract. Session and CSRF
// were checked before the body was read.
func (s *Server) createFeedback(w http.ResponseWriter, r *http.Request) {
	backend, ok := s.store.(FeedbackBackend)
	if !ok {
		s.writeError(w, http.StatusServiceUnavailable, "feedback_unavailable")
		return
	}
	if r.ContentLength > maxFeedbackMultipartBytes {
		s.writeError(w, http.StatusRequestEntityTooLarge, "feedback_too_large")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxFeedbackMultipartBytes)
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			s.writeError(w, http.StatusRequestEntityTooLarge, "feedback_too_large")
			return
		}
		s.writeError(w, http.StatusBadRequest, "feedback_invalid")
		return
	}
	if r.MultipartForm == nil {
		s.writeError(w, http.StatusBadRequest, "feedback_invalid")
		return
	}
	defer r.MultipartForm.RemoveAll()
	values := map[string]any{}
	for _, key := range []string{"title", "area", "current", "expected", "acceptance", "priority"} {
		if list := r.MultipartForm.Value[key]; len(list) > 0 {
			values[key] = list[0]
		}
	}
	var image []byte
	if files := r.MultipartForm.File["image"]; len(files) > 0 {
		file, err := files[0].Open()
		if err != nil {
			s.writeError(w, http.StatusBadRequest, "feedback_invalid")
			return
		}
		image, err = io.ReadAll(io.LimitReader(file, maxFeedbackUploadBytes+1))
		_ = file.Close()
		if err != nil {
			s.writeError(w, http.StatusBadRequest, "feedback_invalid")
			return
		}
		if len(image) > maxFeedbackUploadBytes {
			s.writeFeedbackInvalid(w, "too_large")
			return
		}
	}
	value, err := backend.CreateFeedbackRequest(values, image)
	if err != nil {
		if feedbackInvalidReasons[err.Error()] {
			s.writeFeedbackInvalid(w, err.Error())
			return
		}
		// Storage, task creation and other internal failures are not client
		// validation errors; do not invite a retry with the same payload.
		s.writeError(w, http.StatusInternalServerError, "feedback_unavailable")
		return
	}
	status := http.StatusOK
	if accepted, _ := value["accepted"].(bool); accepted {
		status = http.StatusCreated
	}
	s.writeJSON(w, status, safeMap(value), nil)
}

var feedbackInvalidReasons = map[string]bool{
	"title_short": true, "title_long": true, "area_invalid": true,
	"current_short": true, "expected_short": true, "acceptance_short": true,
	"priority_invalid": true, "image_invalid": true, "too_large": true, "too_long": true,
}

func (s *Server) writeFeedbackInvalid(w http.ResponseWriter, reason string) {
	// Malformed requests share the feedback_invalid code. A deterministic reason
	// is included separately so the UI can translate feedback.reason.<code>.
	if feedbackInvalidReasons[reason] {
		s.writeJSON(w, http.StatusBadRequest, map[string]any{"error": "feedback_invalid", "reason": reason}, nil)
		return
	}
	s.writeError(w, http.StatusBadRequest, "feedback_invalid")
}

func (s *Server) feedbackGET(w http.ResponseWriter, r *http.Request) {
	backend, ok := s.store.(FeedbackBackend)
	if !ok {
		s.writeError(w, http.StatusServiceUnavailable, "feedback_unavailable")
		return
	}
	path := r.URL.Path
	if path == "/api/feedback" {
		value, err := backend.ListFeedback()
		if err != nil {
			s.writeError(w, http.StatusInternalServerError, "feedback_unavailable")
			return
		}
		s.writeJSON(w, http.StatusOK, safeMap(value), nil)
		return
	}
	rest := strings.TrimPrefix(path, "/api/feedback/")
	if strings.HasSuffix(rest, "/image") {
		id := strings.TrimSuffix(rest, "/image")
		if !safeFeedbackID.MatchString(id) {
			s.writeError(w, http.StatusNotFound, "feedback_unknown")
			return
		}
		raw, err := backend.FeedbackImage(id)
		if err != nil {
			s.writeError(w, http.StatusNotFound, "feedback_image_unavailable")
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Content-Length", strconv.Itoa(len(raw)))
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Content-Disposition", `inline; filename="screenshot.png"`)
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(raw)
		return
	}
	if !safeFeedbackID.MatchString(rest) {
		s.writeError(w, http.StatusNotFound, "feedback_unknown")
		return
	}
	value, err := backend.FeedbackDetail(rest)
	if err != nil {
		s.writeError(w, http.StatusNotFound, "feedback_unknown")
		return
	}
	s.writeJSON(w, http.StatusOK, safeMap(value), nil)
}
