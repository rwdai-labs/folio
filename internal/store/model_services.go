package store

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	modelServicesMaxBytes       = 1 << 20
	modelServicesMaxKeyBytes    = 4096
	modelServicesMaxConnections = 16
	modelServicesMaxOverrides   = 64
	modelServicesMaxModels      = 500
	modelServicesMaxModelID     = 512
)

type resolvedModelBinding struct {
	Connection string
	Model      string
	BaseURL    string
	Key        string
	Provider   string
}

var modelServiceIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,63}$`)

func (s *Store) ModelServices() (map[string]any, error) {
	s.modelServicesMu.Lock()
	defer s.modelServicesMu.Unlock()
	return s.loadModelServicesLocked()
}

func (s *Store) loadModelServicesLocked() (map[string]any, error) {
	if err := modelServicesRegularOrAbsent(s.modelServicesPath); err != nil {
		return nil, errStore("model_services_unavailable")
	}
	info, statErr := os.Lstat(s.modelServicesPath)
	if statErr == nil && (info.Size() > modelServicesMaxBytes || info.Mode().Perm()&0o077 != 0) {
		return nil, errStore("model_services_invalid")
	}
	raw, err := os.ReadFile(s.modelServicesPath)
	if errors.Is(err, os.ErrNotExist) {
		return emptyModelServices(), nil
	}
	if err != nil || len(raw) > modelServicesMaxBytes {
		return nil, errStore("model_services_unavailable")
	}
	var value any
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	if err := dec.Decode(&value); err != nil {
		return nil, errStore("model_services_invalid")
	}
	var extra any
	if err := dec.Decode(&extra); err == nil {
		return nil, errStore("model_services_invalid")
	} else if err != io.EOF {
		return nil, errStore("model_services_invalid")
	}
	m, ok := value.(map[string]any)
	if !ok {
		return nil, errStore("model_services_invalid")
	}
	if err := validateModelServices(m); err != nil {
		return nil, err
	}
	return s.redactModelServices(m), nil
}

func emptyModelServices() map[string]any {
	return map[string]any{"version": 1, "connections": map[string]any{}, "defaults": map[string]any{"assistant": map[string]any{}, "report": map[string]any{}, "maintenance": map[string]any{}}, "overrides": map[string]any{"report": map[string]any{}}}
}

func (s *Store) PutModelServices(payload map[string]any) (map[string]any, error) {
	s.modelServicesMu.Lock()
	defer s.modelServicesMu.Unlock()
	if err := validateModelServices(payload); err != nil {
		return nil, err
	}
	clean := sanitizeModelServices(payload)
	encoded, err := json.Marshal(clean)
	if err != nil || len(encoded) > modelServicesMaxBytes {
		return nil, errStore("model_services_invalid")
	}
	connections := payload["connections"].(map[string]any)
	for id, raw := range connections {
		entry := raw.(map[string]any)
		if key, ok := entry["api_key"].(string); ok {
			if err := validateModelServiceKey(key); err != nil {
				return nil, err
			}
		}
		if err := modelServicesRegularOrAbsent(filepath.Join(s.modelServicesSecretsDir, id)); err != nil {
			return nil, errStore("model_service_key_unavailable")
		}
	}
	if err := modelServicesRegularOrAbsent(s.modelServicesPath); err != nil {
		return nil, errStore("model_services_unavailable")
	}
	if err := ensurePrivateDir(filepath.Dir(s.modelServicesPath)); err != nil {
		return nil, errStore("model_services_unavailable")
	}
	if err := ensurePrivateDir(s.modelServicesSecretsDir); err != nil {
		return nil, errStore("model_services_unavailable")
	}
	previousKeys := map[string][]byte{}
	for id, raw := range connections {
		entry := raw.(map[string]any)
		if key, ok := entry["api_key"].(string); ok && strings.TrimSpace(key) != "" {
			prior, err := os.ReadFile(filepath.Join(s.modelServicesSecretsDir, id))
			if err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, errStore("model_service_key_unavailable")
			}
			previousKeys[id] = prior
		}
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		for id, old := range previousKeys {
			path := filepath.Join(s.modelServicesSecretsDir, id)
			if old == nil {
				_ = os.Remove(path)
			} else {
				_ = writeAtomic(path, old, 0o600)
			}
		}
	}()
	for id, raw := range connections {
		entry := raw.(map[string]any)
		if key, ok := entry["api_key"].(string); ok && strings.TrimSpace(key) != "" {
			key = strings.TrimSpace(key)
			if len([]byte(key)) > modelServicesMaxKeyBytes || strings.ContainsAny(key, "\r\n\x00") {
				return nil, errStore("model_service_key_invalid")
			}
			if err := writeAtomic(filepath.Join(s.modelServicesSecretsDir, id), []byte(key+"\n"), 0o600); err != nil {
				return nil, errStore("model_service_key_unavailable")
			}
		}
	}
	if err := writeAtomic(s.modelServicesPath, append(encoded, '\n'), 0o600); err != nil {
		return nil, errStore("model_services_unavailable")
	}
	committed = true
	return s.redactModelServices(clean), nil
}

func modelServicesRegularOrAbsent(path string) error {
	for dir := filepath.Dir(path); ; dir = filepath.Dir(dir) {
		info, err := os.Lstat(dir)
		if err == nil && (!info.IsDir() || info.Mode()&os.ModeSymlink != 0) {
			return errors.New("invalid parent")
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if filepath.Dir(dir) == dir {
			break
		}
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("not regular")
	}
	return nil
}

func validateModelServiceKey(key string) error {
	if len(key) > modelServicesMaxKeyBytes || strings.ContainsAny(key, "\r\n\x00") {
		return errStore("model_service_key_invalid")
	}
	return nil
}

func sanitizeModelServices(value map[string]any) map[string]any {
	out := cloneMap(value)
	if defaults, ok := out["defaults"].(map[string]any); ok {
		for _, purpose := range []string{"assistant", "report", "maintenance"} {
			if defaults[purpose] == nil {
				defaults[purpose] = map[string]any{}
			}
		}
	}
	if cs, ok := out["connections"].(map[string]any); ok {
		for _, raw := range cs {
			if c, ok := raw.(map[string]any); ok {
				delete(c, "api_key")
				delete(c, "key_present")
			}
		}
	}
	return out
}

func (s *Store) redactModelServices(value map[string]any) map[string]any {
	out := cloneMap(value)
	if cs, ok := out["connections"].(map[string]any); ok {
		for id, raw := range cs {
			if c, ok := raw.(map[string]any); ok {
				delete(c, "api_key")
				_, c["key_present"] = s.readModelServiceKey(id)
			}
		}
	}
	return out
}

func (s *Store) readModelServiceKey(id string) (string, bool) {
	key, err := s.loadModelServiceKey(id)
	return key, err == nil && key != ""
}

func (s *Store) loadModelServiceKey(id string) (string, error) {
	info, err := os.Lstat(s.modelServicesSecretsDir)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errStore("model_service_key_unavailable")
	}
	path := filepath.Join(s.modelServicesSecretsDir, id)
	if err := modelServicesRegularOrAbsent(path); err != nil {
		return "", errStore("model_service_key_unavailable")
	}
	info, err = os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > modelServicesMaxKeyBytes+1 || info.Mode().Perm()&0o077 != 0 {
		return "", errStore("model_service_key_unavailable")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", errStore("model_service_key_unavailable")
	}
	key := strings.TrimSpace(string(raw))
	if err := validateModelServiceKey(key); err != nil {
		return "", err
	}
	if key == "" {
		return "", errStore("model_service_key_invalid")
	}
	return key, nil
}

func (s *Store) resolveModelBinding(purpose, taskID string) (*resolvedModelBinding, error) {
	s.modelServicesMu.Lock()
	defer s.modelServicesMu.Unlock()
	cfg, err := s.loadModelServicesLocked()
	if err != nil {
		return nil, err
	}
	var binding map[string]any
	if purpose == "report" && taskID != "" {
		if ovs, ok := cfg["overrides"].(map[string]any); ok {
			if reports, ok := ovs["report"].(map[string]any); ok {
				if x, ok := reports[taskID].(map[string]any); ok {
					binding = x
				}
			}
		}
	}
	if binding == nil {
		if defs, ok := cfg["defaults"].(map[string]any); ok {
			if x, ok := defs[purpose].(map[string]any); ok {
				binding = x
			}
		}
	}
	if binding == nil {
		return nil, nil
	}
	connection, _ := binding["connection"].(string)
	model, _ := binding["model"].(string)
	if connection == "" || model == "" {
		return nil, nil
	}
	cs, _ := cfg["connections"].(map[string]any)
	c, ok := cs[connection].(map[string]any)
	if !ok {
		return nil, nil
	}
	base, _ := c["base_url"].(string)
	key, err := s.loadModelServiceKey(connection)
	if err != nil {
		return nil, err
	}
	if key == "" {
		return nil, errStore("model_service_key_unavailable")
	}
	return &resolvedModelBinding{Connection: connection, Model: model, BaseURL: base, Key: key}, nil
}

func (s *Store) TestModelService(payload map[string]any) (map[string]any, error) {
	if err := validateTestPayload(payload); err != nil {
		return nil, err
	}
	id, _ := payload["connection"].(string)
	s.modelServicesMu.Lock()
	cfg, err := s.loadModelServicesLocked()
	if err != nil {
		s.modelServicesMu.Unlock()
		return nil, err
	}
	cs, _ := cfg["connections"].(map[string]any)
	c, _ := cs[id].(map[string]any)
	base, _ := c["base_url"].(string)
	key, keyErr := s.loadModelServiceKey(id)
	s.modelServicesMu.Unlock()
	providedKey, _ := payload["api_key"].(string)
	if keyErr != nil && strings.TrimSpace(providedKey) == "" {
		return nil, keyErr
	}
	if v, ok := payload["base_url"].(string); ok && strings.TrimSpace(v) != "" {
		base = strings.TrimSpace(v)
	}
	if v, ok := payload["api_key"].(string); ok {
		if err := validateModelServiceKey(v); err != nil {
			return nil, err
		}
		if strings.TrimSpace(v) != "" {
			key = strings.TrimSpace(v)
		}
	}
	u, err := modelServiceURL(base)
	if err != nil {
		return nil, errStore("model_service_unreachable")
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/models"
	req, _ := http.NewRequest(http.MethodGet, u.String(), nil)
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	client := &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Do(req)
	if err != nil {
		return nil, errStore("model_service_unreachable")
	}
	defer resp.Body.Close()
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return nil, errStore("model_service_unauthorized")
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, errStore("model_service_unreachable")
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, modelServicesMaxBytes+1))
	if err != nil || len(body) > modelServicesMaxBytes {
		return nil, errStore("model_service_invalid_response")
	}
	var doc struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	dec := json.NewDecoder(strings.NewReader(string(body)))
	if err := dec.Decode(&doc); err != nil || doc.Data == nil || len(doc.Data) > modelServicesMaxModels {
		return nil, errStore("model_service_invalid_response")
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		return nil, errStore("model_service_invalid_response")
	}
	models := make([]any, 0, len(doc.Data))
	for _, item := range doc.Data {
		if item.ID == "" || len(item.ID) > modelServicesMaxModelID {
			return nil, errStore("model_service_invalid_response")
		}
		models = append(models, map[string]any{"id": item.ID})
	}
	return map[string]any{"reachable": true, "inference_tested": false, "models": models, "checked_at": time.Now().UTC().Format(time.RFC3339)}, nil
}

func validateTestPayload(m map[string]any) error {
	if m == nil {
		return errStore("model_services_invalid")
	}
	for k := range m {
		if k != "connection" && k != "base_url" && k != "api_key" {
			return errStore("model_services_invalid")
		}
	}
	id, ok := m["connection"].(string)
	if !ok || !modelServiceIDPattern.MatchString(id) {
		return errStore("model_services_invalid")
	}
	for _, k := range []string{"base_url", "api_key"} {
		if v, ok := m[k]; ok {
			if _, yes := v.(string); !yes {
				return errStore("model_services_invalid")
			}
		}
	}
	return nil
}

func modelServicesVersion(value any) (int, bool) {
	switch v := value.(type) {
	case int:
		return v, true
	case int64:
		return int(v), int64(int(v)) == v
	case float64:
		return int(v), v == float64(int(v))
	case json.Number:
		n, err := v.Int64()
		return int(n), err == nil && int64(int(n)) == n
	default:
		return 0, false
	}
}

func validateModelServices(m map[string]any) error {
	if m == nil {
		return errStore("model_services_invalid")
	}
	for k := range m {
		if k != "version" && k != "connections" && k != "defaults" && k != "overrides" {
			return errStore("model_services_invalid")
		}
	}
	v, ok := modelServicesVersion(m["version"])
	if !ok || v != 1 {
		return errStore("model_services_invalid")
	}
	cs, ok := m["connections"].(map[string]any)
	if !ok || len(cs) > modelServicesMaxConnections {
		return errStore("model_services_invalid")
	}
	for id, raw := range cs {
		if !modelServiceIDPattern.MatchString(id) {
			return errStore("model_services_invalid")
		}
		c, ok := raw.(map[string]any)
		if !ok {
			return errStore("model_services_invalid")
		}
		for k := range c {
			if k != "name" && k != "base_url" && k != "api_key" {
				return errStore("model_services_invalid")
			}
		}
		if name, ok := c["name"].(string); !ok || strings.TrimSpace(name) == "" || len(name) > 128 || strings.ContainsAny(name, "\r\n\x00") {
			return errStore("model_services_invalid")
		}
		base, ok := c["base_url"].(string)
		if !ok {
			return errStore("model_services_invalid")
		}
		if _, err := modelServiceURL(base); err != nil {
			return errStore("model_services_invalid")
		}
		if key, exists := c["api_key"]; exists {
			if _, ok := key.(string); !ok {
				return errStore("model_services_invalid")
			}
		}
	}
	if err := validateBindings(m, "defaults", false); err != nil {
		return err
	}
	if err := validateBindings(m, "overrides", true); err != nil {
		return err
	}
	defaults := m["defaults"].(map[string]any)
	overrides := m["overrides"].(map[string]any)["report"].(map[string]any)
	for _, bindings := range []map[string]any{defaults, overrides} {
		for _, value := range bindings {
			if value == nil {
				continue
			}
			b := value.(map[string]any)
			if connection, _ := b["connection"].(string); connection != "" {
				if _, ok := cs[connection]; !ok {
					return errStore("model_services_invalid")
				}
			}
		}
	}
	return nil
}

func validateBindings(root map[string]any, field string, overrides bool) error {
	x, ok := root[field].(map[string]any)
	if !ok {
		return errStore("model_services_invalid")
	}
	if overrides {
		for key := range x {
			if key != "report" {
				return errStore("model_services_invalid")
			}
		}
		r, ok := x["report"].(map[string]any)
		if !ok || len(r) > modelServicesMaxOverrides {
			return errStore("model_services_invalid")
		}
		return validateBindingMap(r)
	}
	for key := range x {
		if key != "assistant" && key != "report" && key != "maintenance" {
			return errStore("model_services_invalid")
		}
	}
	for _, p := range []string{"assistant", "report", "maintenance"} {
		if x[p] == nil {
			continue
		}
		b, ok := x[p].(map[string]any)
		if !ok {
			return errStore("model_services_invalid")
		}
		if err := validateBinding(b); err != nil {
			return err
		}
	}
	return nil
}
func validateBindingMap(m map[string]any) error {
	for id, raw := range m {
		if len(id) == 0 || len(id) > 128 {
			return errStore("model_services_invalid")
		}
		b, ok := raw.(map[string]any)
		if !ok {
			return errStore("model_services_invalid")
		}
		if err := validateBinding(b); err != nil {
			return err
		}
	}
	return nil
}
func validateBinding(m map[string]any) error {
	if len(m) == 0 {
		return nil
	}
	for k := range m {
		if k != "connection" && k != "model" {
			return errStore("model_services_invalid")
		}
	}
	c, ok := m["connection"].(string)
	if model, yes := m["model"].(string); ok && yes && c == "" && model == "" {
		return nil
	}
	if !ok || !modelServiceIDPattern.MatchString(c) {
		return errStore("model_services_invalid")
	}
	model, ok := m["model"].(string)
	if !ok || !modelRE.MatchString(model) {
		return errStore("model_services_invalid")
	}
	return nil
}

func modelServiceURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || len(raw) > 2048 || raw != strings.TrimSpace(raw) || strings.ContainsAny(raw, "\r\n\x00") || u.Host == "" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.Contains(raw, "#") || u.Scheme != "https" && u.Scheme != "http" {
		return nil, errors.New("invalid url")
	}
	if u.Scheme == "http" {
		host := u.Hostname()
		ip := net.ParseIP(host)
		if !strings.EqualFold(host, "localhost") && (ip == nil || !(ip.IsPrivate() || ip.IsLoopback())) {
			return nil, errors.New("plain http not local")
		}
	}
	return u, nil
}
func cloneMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		if m, ok := v.(map[string]any); ok {
			out[k] = cloneMap(m)
		} else {
			out[k] = v
		}
	}
	return out
}
