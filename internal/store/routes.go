package store

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

const maxEventRoutes = 64

var eventTypeRE = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,63}$`)

type routesFile struct {
	Version int          `json:"version"`
	Routes  []eventRoute `json:"routes"`
}

func routesPathFor(data string) string { return filepath.Join(data, "routes.json") }

func defaultRoutesFile() routesFile {
	return routesFile{Version: 1, Routes: []eventRoute{
		{Source: "scheduler", Type: "tick", Target: "action:" + taskKindMaintenance},
		{Source: "console", Type: "owner.message", Target: "agent:main"},
		{Source: "weixin", Type: "owner.message", Target: "agent:main"},
		{Source: "task", Type: "failed", Target: "notify:owner"},
	}}
}

func loadEventRoutes(data string) (routesFile, error) {
	path := routesPathFor(data)
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		return defaultRoutesFile(), nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > maxPipelineFileBytes {
		return routesFile{}, errStore("routes are unavailable")
	}
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) > maxPipelineFileBytes {
		return routesFile{}, errStore("routes are unavailable")
	}
	var file routesFile
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&file); err != nil {
		return routesFile{}, errStore("routes are invalid")
	}
	if err := validateRoutes(&file); err != nil {
		return routesFile{}, err
	}
	return file, nil
}

func validateRoutes(file *routesFile) error {
	if file.Version != 1 || len(file.Routes) > maxEventRoutes {
		return errStore("routes are invalid")
	}
	seen := map[string]bool{}
	for index := range file.Routes {
		route := &file.Routes[index]
		route.Source = strings.TrimSpace(route.Source)
		route.Type = strings.TrimSpace(route.Type)
		route.Target = strings.TrimSpace(route.Target)
		if !pipelineStepRE.MatchString(route.Source) || !eventTypeRE.MatchString(route.Type) {
			return errStore("routes are invalid")
		}
		key := route.Source + "\x00" + route.Type
		if seen[key] {
			return errStore("routes are invalid")
		}
		seen[key] = true
		if !validRouteTarget(route.Target) {
			return errStore("routes are invalid")
		}
	}
	return nil
}

func validRouteTarget(target string) bool {
	switch target {
	case "agent:main", "notify:owner", "audit:only":
		return true
	}
	return validActionTarget(target)
}

// eventRouteFor resolves a route from routes.json, falling back to the
// built-in table when the file is absent. An explicit trusted target wins.
func (s *Store) eventRouteFor(e event) (string, bool, error) {
	if !validEventTrust(e.Trust) {
		return "", false, nil
	}
	if e.route != "" {
		return e.route, validRouteTarget(e.route), nil
	}
	file, err := loadEventRoutes(s.dataDir)
	if err != nil {
		return "", false, err
	}
	for _, route := range file.Routes {
		if route.Source == e.Source && route.Type == e.Type {
			return route.Target, validRouteTarget(route.Target), nil
		}
	}
	return "", false, nil
}

func (s *Store) ListRoutes() (map[string]any, error) {
	file, err := loadEventRoutes(s.dataDir)
	if err != nil {
		return nil, err
	}
	return map[string]any{"version": file.Version, "routes": file.Routes}, nil
}

func (s *Store) PutRoutesJSON(raw []byte) (map[string]any, error) {
	if len(raw) == 0 || len(raw) > maxPipelineFileBytes {
		return nil, errStore("routes are invalid")
	}
	var file routesFile
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&file); err != nil {
		return nil, errStore("routes are invalid")
	}
	if err := validateRoutes(&file); err != nil {
		return nil, err
	}
	encoded, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return nil, errStore("routes are invalid")
	}
	s.actionMu.Lock()
	defer s.actionMu.Unlock()
	if err := writeAtomic(routesPathFor(s.dataDir), append(encoded, byte(10)), 0o600); err != nil {
		return nil, err
	}
	return s.ListRoutes()
}
