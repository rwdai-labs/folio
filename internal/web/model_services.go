package web

import "net/http"

// writeModelServicesResult keeps configuration and upstream errors out of HTTP
// responses. The store already returns a secret-free projection on success.
func (s *Server) writeModelServicesResult(w http.ResponseWriter, value map[string]any, err error) {
	if err == nil {
		s.writeJSON(w, http.StatusOK, safeMap(value), nil)
		return
	}
	status, code := http.StatusInternalServerError, "model_services_unavailable"
	switch err.Error() {
	case "model_services_invalid", "model_service_key_invalid":
		status, code = http.StatusBadRequest, err.Error()
	case "model_service_unauthorized", "model_service_unreachable", "model_service_invalid_response":
		status, code = http.StatusBadGateway, err.Error()
	case "model_service_key_unavailable":
		code = err.Error()
	}
	s.writeError(w, status, code)
}
