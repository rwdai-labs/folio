// Package plugin hosts built-in capability plugins.
//
// A plugin is a Go package under plugins/ that main wires into the service. It
// owns one concern end to end: its backend routes, its frontend assets, and
// optionally the storage boundary for the context repository. It never reaches
// into Folio's state; the host gives it a request body and takes back data.
package plugin

import (
	"context"
	"io/fs"
	"net/url"

	"folio/internal/storage"
)

// Request is the bounded input of one plugin route. The host has already
// authenticated the browser session and enforced CSRF; the body is untrusted
// data, capped like any other admin request.
type Request struct {
	Body  map[string]any
	Query url.Values
	// Exec is the host's safe process boundary. A plugin never calls exec
	// itself, so credential and environment handling stay in one place.
	Exec storage.Executor
}

// Handler is one route. It returns data, not a response: the host owns JSON
// encoding, redaction, and error mapping.
type Handler func(ctx context.Context, request Request) (map[string]any, error)

// Route is mounted at /api/plugins/{id}{Path} for the given method.
type Route struct {
	Method  string // GET or POST
	Path    string // "/auth/start" (empty is the plugin root)
	Handler Handler
}

// Plugin is one built-in capability.
type Plugin interface {
	ID() string
	// Title is the untranslated default name; the frontend translates.
	Title() string
	// Assets is served read-only at /plugins/{id}/.
	Assets() fs.FS
	// Routes are mounted under /api/plugins/{id}.
	Routes() []Route
	// Storage returns the remote backend, or nil when the plugin only adds UI.
	Storage() storage.Remote
}

// Registry is the immutable set of plugins compiled into this binary.
type Registry struct {
	order []Plugin
	byID  map[string]Plugin
}

func NewRegistry(plugins ...Plugin) *Registry {
	registry := &Registry{byID: map[string]Plugin{}}
	for _, item := range plugins {
		if item == nil {
			continue
		}
		registry.order = append(registry.order, item)
		registry.byID[item.ID()] = item
	}
	return registry
}

func (r *Registry) List() []Plugin {
	if r == nil {
		return nil
	}
	return append([]Plugin(nil), r.order...)
}

func (r *Registry) Get(id string) (Plugin, bool) {
	if r == nil {
		return nil, false
	}
	item, ok := r.byID[id]
	return item, ok
}

// Remote returns the storage backend of the first plugin that provides one.
func (r *Registry) Remote() storage.Remote {
	for _, item := range r.order {
		if remote := item.Storage(); remote != nil {
			return remote
		}
	}
	return nil
}
