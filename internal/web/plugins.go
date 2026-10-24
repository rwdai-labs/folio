package web

import (
	"context"
	"io/fs"
	"net/http"
	"path"
	"strconv"
	"strings"
	"time"

	"folio/internal/plugin"
)

const (
	maxPluginAssetBytes = 1 << 20
	pluginCallTimeout   = 60 * time.Second
)

var pluginAssetTypes = map[string]string{
	".js":   "text/javascript; charset=utf-8",
	".css":  "text/css; charset=utf-8",
	".json": "application/json; charset=utf-8",
	".svg":  "image/svg+xml",
	".png":  "image/png",
}

// pluginList is the discovery surface: the shell asks which plugins are
// compiled in and loads their frontend scripts. No plugin state is exposed.
func (s *Server) pluginList(w http.ResponseWriter) {
	items := make([]map[string]any, 0, 4)
	for _, item := range s.opts.Plugins.List() {
		items = append(items, map[string]any{"id": item.ID(), "title": item.Title()})
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"plugins": items}, nil)
}

// pluginAPI dispatches /api/plugins/{id}{path}. The session and CSRF checks
// already happened for POST; a GET still requires a session because every
// plugin route is an admin surface.
func (s *Server) pluginAPI(w http.ResponseWriter, r *http.Request, payload map[string]any) {
	if _, _, ok := s.session(r); !ok {
		s.writeError(w, http.StatusUnauthorized, "authentication_required")
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/plugins/")
	id, suffix, _ := strings.Cut(rest, "/")
	item, ok := s.opts.Plugins.Get(id)
	if !ok {
		s.writeError(w, http.StatusNotFound, "not_found")
		return
	}
	routePath := "/" + suffix
	if suffix == "" {
		routePath = "/"
	}
	for _, route := range item.Routes() {
		if route.Method != r.Method || route.Path != routePath {
			continue
		}
		ctx, cancel := context.WithTimeout(r.Context(), pluginCallTimeout)
		defer cancel()
		value, err := route.Handler(ctx, plugin.Request{
			Body:  payload,
			Query: r.URL.Query(),
			Exec:  s.store.Executor(),
		})
		if err != nil {
			s.writeError(w, http.StatusBadGateway, "plugin_error")
			return
		}
		s.writeJSON(w, http.StatusOK, safeMap(value), nil)
		return
	}
	s.writeError(w, http.StatusNotFound, "not_found")
}

// pluginAsset serves a plugin's own frontend from its embedded filesystem.
// The path is resolved inside the plugin root, never against the host disk.
func (s *Server) pluginAsset(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/plugins/")
	id, name, ok := strings.Cut(rest, "/")
	if !ok || name == "" {
		s.writeError(w, http.StatusNotFound, "not_found")
		return
	}
	item, found := s.opts.Plugins.Get(id)
	if !found || item.Assets() == nil {
		s.writeError(w, http.StatusNotFound, "not_found")
		return
	}
	cleaned := path.Clean("/" + name)
	if cleaned == "/" || strings.Contains(cleaned, "..") {
		s.writeError(w, http.StatusNotFound, "not_found")
		return
	}
	cleaned = strings.TrimPrefix(cleaned, "/")
	contentType, allowed := pluginAssetTypes[path.Ext(cleaned)]
	if !allowed {
		s.writeError(w, http.StatusNotFound, "not_found")
		return
	}
	data, err := fs.ReadFile(item.Assets(), cleaned)
	if err != nil || len(data) > maxPluginAssetBytes {
		s.writeError(w, http.StatusNotFound, "not_found")
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; object-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}
