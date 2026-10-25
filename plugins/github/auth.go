package github

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"folio/internal/plugin"
	"folio/internal/storage"
)

var (
	deviceCodeRE = regexp.MustCompile(`^[A-Za-z0-9._~:/_+\-]+$`)
	userCodeRE   = regexp.MustCompile(`(?i)^[A-Z0-9]{4}-[A-Z0-9]{4}$`)
)

const (
	repositoriesPerPage = 100
	// maxRepositories is a safety bound, not a page size: the picker searches
	// the whole loaded list, so an owner with many repositories still finds an
	// older one without paging through GitHub by hand.
	maxRepositories    = 1000
	maxRepositoryPages = maxRepositories / repositoriesPerPage
	maxAPIPayload      = 2 * 1024 * 1024
)

// deviceFlow is the private state of one sign-in attempt.
type deviceFlow struct {
	Code      string    `json:"device_code"`
	UserCode  string    `json:"user_code"`
	ExpiresAt time.Time `json:"expires_at"`
	Interval  int       `json:"interval"`
	LastPoll  time.Time `json:"last_poll_at,omitempty"`
}

func (p *Plugin) startAuth(ctx context.Context, request plugin.Request) (map[string]any, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if flow, ok := p.loadFlow(); ok && flow.ExpiresAt.After(time.Now()) {
		return p.flowResponse("pending", flow), nil
	}
	if mode := p.authMode(ctx, request.Exec); mode != "missing" {
		return map[string]any{"status": "authenticated", "mode": mode}, nil
	}
	flow, err := p.beginFlow(ctx)
	if err != nil {
		return map[string]any{"status": "error", "message": "GitHub sign-in could not be started"}, nil
	}
	if err := p.saveFlow(flow); err != nil {
		return map[string]any{"status": "error", "message": "GitHub sign-in could not be started"}, nil
	}
	return p.flowResponse("pending", flow), nil
}

func (p *Plugin) pollAuth(ctx context.Context, request plugin.Request) (map[string]any, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	flow, ok := p.loadFlow()
	if !ok {
		if mode := p.authMode(ctx, request.Exec); mode != "missing" {
			return map[string]any{"status": "authenticated", "mode": mode}, nil
		}
		return map[string]any{"status": "signed_out"}, nil
	}
	if !flow.ExpiresAt.After(time.Now()) {
		p.clearFlow()
		return map[string]any{"status": "error", "message": "GitHub sign-in expired"}, nil
	}
	now := time.Now()
	if !flow.LastPoll.IsZero() && now.Before(flow.LastPoll.Add(time.Duration(flow.Interval)*time.Second)) {
		return p.flowResponse("pending", flow), nil
	}
	flow.LastPoll = now
	_ = p.saveFlow(flow)
	result, err := p.tokenRequest(ctx, flow.Code)
	if err != nil {
		return map[string]any{"status": "error", "message": "GitHub sign-in could not be checked"}, nil
	}
	if token, ok := result["access_token"].(string); ok && tokenRE.MatchString(token) {
		if err := p.storeToken(ctx, request.Exec, token); err != nil {
			p.clearFlow()
			return map[string]any{"status": "error", "message": "GitHub sign-in could not be stored"}, nil
		}
		p.clearFlow()
		return map[string]any{"status": "authenticated", "mode": p.authMode(ctx, request.Exec)}, nil
	}
	switch stringValue(result["error"]) {
	case "authorization_pending":
		return p.flowResponse("pending", flow), nil
	case "slow_down":
		flow.Interval += 5
		if flow.Interval > 60 {
			flow.Interval = 60
		}
		_ = p.saveFlow(flow)
		return p.flowResponse("pending", flow), nil
	default:
		p.clearFlow()
		return map[string]any{"status": "error", "message": "GitHub sign-in was denied"}, nil
	}
}

func (p *Plugin) logout(ctx context.Context, request plugin.Request) (map[string]any, error) {
	p.mu.Lock()
	p.clearFlow()
	p.mu.Unlock()
	if gh, err := request.Exec.LookPath("gh"); err == nil {
		// gh auth logout has no confirmation flag: with --hostname and a single
		// stored account it signs out without prompting, even without a TTY.
		out, runErr := request.Exec.Exec(ctx, []string{gh, "auth", "logout", "--hostname", credentialHost}, p.dataDir, "", 30*time.Second)
		if runErr != nil || out.Code != 0 {
			// A missing session is already signed out.
			if p.authMode(ctx, request.Exec) != "missing" {
				return nil, errors.New("GitHub sign-out did not complete")
			}
		}
	}
	p.invalidateStatus()
	mode := p.authMode(ctx, request.Exec)
	status := "signed_out"
	if mode != "missing" {
		status = "authenticated"
	}
	return map[string]any{"status": status, "mode": mode}, nil
}

func (p *Plugin) status(ctx context.Context, request plugin.Request) (map[string]any, error) {
	p.mu.Lock()
	flow, pending := p.loadFlow()
	p.mu.Unlock()
	if pending && flow.ExpiresAt.After(time.Now()) {
		return p.flowResponse("pending", flow), nil
	}
	mode := p.authMode(ctx, request.Exec)
	if mode == "missing" {
		return map[string]any{"status": "signed_out"}, nil
	}
	return map[string]any{"status": "authenticated", "mode": mode}, nil
}

func (p *Plugin) repositories(ctx context.Context, request plugin.Request) (map[string]any, error) {
	token, err := p.ghToken(ctx, request.Exec)
	if err != nil {
		return nil, err
	}
	list, truncated, err := p.fetchRepositories(ctx, token)
	if err != nil {
		return nil, err
	}
	private := 0
	for _, item := range list {
		if value, ok := item["private"].(bool); ok && value {
			private++
		}
	}
	return map[string]any{
		"repositories": list,
		"count":        len(list),
		"private":      private,
		"truncated":    truncated,
	}, nil
}

// authMode reports where the credential comes from without exposing it. The
// result is cached briefly because /api/state polls it and the check starts a
// process.
func (p *Plugin) authMode(ctx context.Context, exec storage.Executor) string {
	if p.tokenFromEnv() != "" {
		return "token"
	}
	p.statusMu.Lock()
	cached, at := p.statusVal, p.statusAt
	p.statusMu.Unlock()
	if cached != "" && time.Since(at) < 10*time.Second {
		return cached
	}
	mode := "missing"
	if p.ghSignedIn(ctx, exec) {
		mode = "gh"
	}
	p.statusMu.Lock()
	p.statusVal, p.statusAt = mode, time.Now()
	p.statusMu.Unlock()
	return mode
}

func (p *Plugin) beginFlow(ctx context.Context) (*deviceFlow, error) {
	result, err := p.post(ctx, deviceURL, url.Values{
		"client_id": {deviceClientID},
		"scope":     {"repo read:org"},
	})
	if err != nil {
		return nil, err
	}
	code := stringValue(result["device_code"])
	userCode := stringValue(result["user_code"])
	verification := stringValue(result["verification_uri"])
	if verification == "" {
		verification = stringValue(result["verification_uri_complete"])
	}
	parsed, err := url.Parse(verification)
	if !deviceCodeRE.MatchString(code) || len(code) > 1024 || !userCodeRE.MatchString(userCode) ||
		err != nil || parsed.Scheme != "https" || parsed.Host != credentialHost || parsed.Path != "/login/device" ||
		parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("device authorization response is invalid")
	}
	expiresIn, ok := intValue(result["expires_in"])
	interval, intervalOK := intValue(result["interval"])
	if !ok || !intervalOK || expiresIn < 60 || expiresIn > 3600 || interval < 1 || interval > 60 {
		return nil, errors.New("device authorization response is invalid")
	}
	return &deviceFlow{
		Code:      code,
		UserCode:  strings.ToUpper(userCode),
		ExpiresAt: time.Now().Add(time.Duration(expiresIn) * time.Second),
		Interval:  interval,
	}, nil
}

func (p *Plugin) tokenRequest(ctx context.Context, code string) (map[string]any, error) {
	return p.post(ctx, deviceTokenURL, url.Values{
		"client_id":   {deviceClientID},
		"device_code": {code},
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
	})
}

// storeToken writes the token through gh so the standard git credential helper
// can use it. The token goes in on stdin, never in an argument.
func (p *Plugin) storeToken(ctx context.Context, exec storage.Executor, token string) error {
	gh, err := exec.LookPath("gh")
	if err != nil {
		return errors.New("gh is unavailable")
	}
	out, runErr := exec.Exec(ctx, []string{
		gh, "auth", "login", "--hostname", credentialHost, "--git-protocol", "https", "--with-token", "--insecure-storage",
	}, p.dataDir, token+"\n", 30*time.Second)
	if runErr != nil || out.Code != 0 {
		return errors.New("GitHub token could not be stored")
	}
	p.invalidateStatus()
	return nil
}

func (p *Plugin) invalidateStatus() {
	p.statusMu.Lock()
	p.statusVal, p.statusAt = "", time.Time{}
	p.statusMu.Unlock()
}

func (p *Plugin) post(ctx context.Context, endpoint string, form url.Values) (map[string]any, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, errors.New("GitHub authorization service is unavailable")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("User-Agent", "folio-service/1")
	response, err := authClient().Do(request)
	if err != nil {
		return nil, errors.New("GitHub authorization service is unavailable")
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxAuthBodyBytes+1))
	if err != nil || len(raw) > maxAuthBodyBytes || response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, errors.New("GitHub authorization response is invalid")
	}
	var result map[string]any
	if json.Unmarshal(raw, &result) != nil {
		return nil, errors.New("GitHub authorization response is invalid")
	}
	return result, nil
}

// repository is the bounded subset of GitHub's repository payload the picker
// needs. Nothing else from the API response is kept.
type repository struct {
	FullName      string `json:"full_name"`
	Private       bool   `json:"private"`
	Archived      bool   `json:"archived"`
	DefaultBranch string `json:"default_branch"`
}

// fetchRepositories walks every page GitHub offers up to maxRepositories. The
// picker filters this list locally, so private and older repositories must be
// present: a truncated list is reported, never silently shortened.
func (p *Plugin) fetchRepositories(ctx context.Context, token string) ([]map[string]any, bool, error) {
	client := authClient()
	seen := map[string]struct{}{}
	list := make([]map[string]any, 0, 64)
	for page := 1; page <= maxRepositoryPages; page++ {
		items, err := p.repositoryPage(ctx, client, token, page)
		if err != nil {
			return nil, false, err
		}
		for _, item := range items {
			if item.Archived || !validFullName(item.FullName) {
				continue
			}
			if _, ok := seen[item.FullName]; ok {
				continue
			}
			seen[item.FullName] = struct{}{}
			list = append(list, map[string]any{
				"full_name":      item.FullName,
				"private":        item.Private,
				"default_branch": item.DefaultBranch,
			})
			if len(list) >= maxRepositories {
				return list, true, nil
			}
		}
		if len(items) < repositoriesPerPage {
			break
		}
	}
	return list, len(list) >= maxRepositories, nil
}

func (p *Plugin) repositoryPage(ctx context.Context, client *http.Client, token string, page int) ([]repository, error) {
	endpoint, err := url.Parse(apiURL + "/user/repos")
	if err != nil {
		return nil, errors.New("GitHub repository list is unavailable")
	}
	query := endpoint.Query()
	query.Set("per_page", strconv.Itoa(repositoriesPerPage))
	query.Set("page", strconv.Itoa(page))
	query.Set("sort", "updated")
	query.Set("affiliation", "owner,collaborator,organization_member")
	query.Set("visibility", "all")
	endpoint.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, errors.New("GitHub repository list is unavailable")
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("User-Agent", "folio-service/1")
	response, err := client.Do(request)
	if err != nil {
		return nil, errors.New("GitHub repository list is unavailable")
	}
	raw, readErr := io.ReadAll(io.LimitReader(response.Body, maxAPIPayload+1))
	response.Body.Close()
	if readErr != nil || len(raw) > maxAPIPayload || response.StatusCode != http.StatusOK {
		return nil, errors.New("GitHub repository list is unavailable")
	}
	var pageItems []repository
	if json.Unmarshal(raw, &pageItems) != nil {
		return nil, errors.New("GitHub repository list is unavailable")
	}
	return pageItems, nil
}

func (p *Plugin) flowResponse(status string, flow *deviceFlow) map[string]any {
	response := map[string]any{
		"status":           status,
		"verification_uri": "https://github.com/login/device",
		"user_code":        flow.UserCode,
	}
	remaining := int(time.Until(flow.ExpiresAt).Seconds())
	if remaining < 0 {
		remaining = 0
	}
	response["expires_in"] = remaining
	response["interval"] = flow.Interval
	return response
}

func (p *Plugin) loadFlow() (*deviceFlow, bool) {
	if p.pending != nil {
		return p.pending, true
	}
	raw, err := os.ReadFile(p.authPath)
	if err != nil || len(raw) > 16*1024 {
		return nil, false
	}
	var flow deviceFlow
	if json.Unmarshal(raw, &flow) != nil || !deviceCodeRE.MatchString(flow.Code) || !userCodeRE.MatchString(flow.UserCode) ||
		flow.Interval < 1 || flow.Interval > 60 || !flow.ExpiresAt.After(time.Now()) {
		return nil, false
	}
	p.pending = &flow
	return p.pending, true
}

func (p *Plugin) saveFlow(flow *deviceFlow) error {
	raw, err := json.Marshal(flow)
	if err != nil {
		return err
	}
	if err := os.WriteFile(p.authPath, raw, 0o600); err != nil {
		return err
	}
	p.pending = flow
	return os.Chmod(p.authPath, 0o600)
}

func (p *Plugin) clearFlow() {
	p.pending = nil
	_ = os.Remove(p.authPath)
}

func validFullName(value string) bool {
	if strings.Count(value, "/") != 1 {
		return false
	}
	owner, name, _ := strings.Cut(value, "/")
	return repositoryNameRE.MatchString(owner) && repositoryNameRE.MatchString(name)
}

func authClient() *http.Client {
	return &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{Proxy: safeProxy}}
}

// safeProxy follows the standard proxy environment, including NO_PROXY, but
// refuses a proxy URL with embedded credentials.
func safeProxy(request *http.Request) (*url.URL, error) {
	proxy, err := http.ProxyFromEnvironment(request)
	if err != nil || proxy == nil || proxy.User != nil {
		return nil, nil
	}
	return proxy, nil
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}

func intValue(value any) (int, bool) {
	number, ok := value.(float64)
	if !ok || number < 0 || number > 1e6 || number != float64(int(number)) {
		return 0, false
	}
	return int(number), true
}
