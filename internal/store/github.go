package store

import (
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// GitHubStart begins the official GitHub device authorization flow. The
// device code is persisted privately, while only the public verification data
// is returned to the Web layer.
func (s *Store) GitHubStart() (map[string]any, error) {
	return s.withLock(func() (map[string]any, error) {
		if s.restorePendingDeviceFlow() {
			return s.authResponse("pending", ""), nil
		}
		if s.ghAuthenticated() {
			if err := s.setGitHubStatus("authenticated"); err != nil {
				return nil, err
			}
			return map[string]any{"status": "authenticated"}, nil
		}
		if err := s.beginDeviceFlow(); err != nil {
			_ = s.setGitHubStatus("error")
			return nil, errStore("GitHub sign-in could not be started")
		}
		if err := s.setGitHubStatus("pending"); err != nil {
			return nil, err
		}
		return s.authResponse("pending", ""), nil
	})
}

// GitHubPoll checks the device authorization without polling more frequently
// than GitHub's advertised interval.
func (s *Store) GitHubPoll() (map[string]any, error) {
	return s.withLock(func() (map[string]any, error) {
		if s.restorePendingDeviceFlow() {
			if s.ghAuthenticated() {
				s.clearDeviceFlow()
				if err := s.setGitHubStatus("authenticated"); err != nil {
					return nil, err
				}
				return map[string]any{"status": "authenticated"}, nil
			}
			if !s.authDeviceExpiresAt.After(time.Now()) {
				s.clearDeviceFlow()
				_ = s.setGitHubStatus("error")
				return map[string]any{"status": "error", "message": "GitHub sign-in expired"}, nil
			}
			now := time.Now()
			if !s.authDeviceLastPoll.IsZero() && now.Before(s.authDeviceLastPoll.Add(time.Duration(s.authDeviceInterval)*time.Second)) {
				return s.authResponse("pending", ""), nil
			}
			s.authDeviceLastPoll = now
			if err := s.savePendingDeviceFlow(); err != nil {
				return nil, err
			}
			result, err := s.githubTokenRequest()
			if err != nil {
				_ = s.setGitHubStatus("error")
				return map[string]any{"status": "error", "message": "GitHub sign-in could not be checked"}, nil
			}
			if token, ok := result["access_token"].(string); ok && token != "" {
				if err := s.storeGitHubToken(token); err != nil {
					s.clearDeviceFlow()
					_ = s.setGitHubStatus("error")
					return map[string]any{"status": "error", "message": "GitHub sign-in could not be stored"}, nil
				}
				s.clearDeviceFlow()
				if err := s.setGitHubStatus("authenticated"); err != nil {
					return nil, err
				}
				return map[string]any{"status": "authenticated"}, nil
			}
			switch resultString(result, "error", "") {
			case "authorization_pending":
				if err := s.setGitHubStatus("pending"); err != nil {
					return nil, err
				}
				return s.authResponse("pending", ""), nil
			case "slow_down":
				s.authDeviceInterval += 5
				if s.authDeviceInterval > 60 {
					s.authDeviceInterval = 60
				}
				if err := s.savePendingDeviceFlow(); err != nil {
					return nil, err
				}
				if err := s.setGitHubStatus("pending"); err != nil {
					return nil, err
				}
				return s.authResponse("pending", ""), nil
			default:
				s.clearDeviceFlow()
				_ = s.setGitHubStatus("error")
				return map[string]any{"status": "error", "message": "GitHub sign-in was denied"}, nil
			}
		}
		if s.ghAuthenticated() {
			if err := s.setGitHubStatus("authenticated"); err != nil {
				return nil, err
			}
			return map[string]any{"status": "authenticated"}, nil
		}
		if err := s.setGitHubStatus("signed_out"); err != nil {
			return nil, err
		}
		return map[string]any{"status": "signed_out"}, nil
	})
}

// Poll and Logout are the short names used by the HTTP Backend interface.
func (s *Store) Poll() (map[string]any, error) { return s.GitHubPoll() }

// GitHubLogout clears the isolated gh credential store and pending device
// authorization.
func (s *Store) GitHubLogout() (map[string]any, error) {
	return s.withLock(func() (map[string]any, error) {
		s.clearDeviceFlow()
		gh, err := s.executable("gh")
		if err == nil {
			result, runErr := s.runCommand([]string{gh, "auth", "logout", "--hostname", "github.com", "--yes"}, s.dataDir, "", 30*time.Second, false, nil)
			if runErr != nil || result.code != 0 {
				return nil, errStore("GitHub sign-out did not complete")
			}
		}
		if err := s.setGitHubStatus("signed_out"); err != nil {
			return nil, err
		}
		return map[string]any{"status": "signed_out"}, nil
	})
}

func (s *Store) Logout() (map[string]any, error) { return s.GitHubLogout() }

func (s *Store) beginDeviceFlow() error {
	response, err := s.githubRequest(githubDeviceURL, url.Values{
		"client_id": {githubClientID},
		"scope":     {"repo read:org"},
	})
	if err != nil {
		return errStore("GitHub authorization service is unavailable")
	}
	deviceCode, ok := response["device_code"].(string)
	if !ok || !validDeviceCode(deviceCode) {
		return errStore("GitHub device authorization response is invalid")
	}
	userCode, ok := response["user_code"].(string)
	if !ok || !userCodeRE.MatchString(userCode) {
		return errStore("GitHub device authorization response is invalid")
	}
	verification := responseString(response, "verification_uri")
	if verification == "" {
		verification = responseString(response, "verification_uri_complete")
	}
	parsed, err := url.Parse(verification)
	if err != nil || parsed.Scheme != "https" || parsed.Host != "github.com" || parsed.Path != "/login/device" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return errStore("GitHub device authorization response is invalid")
	}
	expiresIn, ok := responseInt(response, "expires_in", 900)
	interval, intervalOK := responseInt(response, "interval", 5)
	if !ok || !intervalOK || expiresIn < 60 || expiresIn > 3600 || interval < 1 || interval > 60 {
		return errStore("GitHub device authorization response is invalid")
	}
	s.authDeviceCode = deviceCode
	s.authDeviceExpiresAt = time.Now().Add(time.Duration(expiresIn) * time.Second)
	s.authDeviceInterval = interval
	s.authDeviceLastPoll = time.Time{}
	s.authDeviceInfo = map[string]string{
		"verification_uri": "https://github.com/login/device",
		"user_code":        strings.ToUpper(userCode),
	}
	return s.savePendingDeviceFlow()
}

func (s *Store) githubTokenRequest() (map[string]any, error) {
	if s.authDeviceCode == "" {
		return nil, errStore("GitHub device authorization is unavailable")
	}
	return s.githubRequest(githubTokenURL, url.Values{
		"client_id":   {githubClientID},
		"device_code": {s.authDeviceCode},
		"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
	})
}

func (s *Store) storeGitHubToken(token string) error {
	if !validDeviceCode(token) {
		return errStore("GitHub token response is invalid")
	}
	gh, err := s.executable("gh")
	if err != nil {
		return errStore("GitHub token could not be stored")
	}
	result, err := s.runCommand([]string{
		gh, "auth", "login", "--hostname", "github.com", "--git-protocol", "https", "--with-token", "--insecure-storage",
	}, s.dataDir, token+"\n", 30*time.Second, false, nil)
	if err != nil || result.code != 0 {
		return errStore("GitHub token could not be stored")
	}
	return nil
}

func (s *Store) githubRequest(endpoint string, form url.Values) (map[string]any, error) {
	request, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, errStore("GitHub authorization service is unavailable")
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	request.Header.Set("User-Agent", "folio-service/1")
	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			Proxy: safeProxyFunc,
		},
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, errStore("GitHub authorization service is unavailable")
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxDevicePayload+1))
	if err != nil || len(raw) > maxDevicePayload || response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, errStore("GitHub authorization response is invalid")
	}
	var result map[string]any
	if json.Unmarshal(raw, &result) == nil {
		return result, nil
	}
	parsed, err := url.ParseQuery(string(raw))
	if err != nil {
		return nil, errStore("GitHub authorization response is invalid")
	}
	result = make(map[string]any)
	for key, values := range parsed {
		if len(values) != 0 {
			result[key] = values[len(values)-1]
		}
	}
	return result, nil
}

func safeProxyFunc(request *http.Request) (*url.URL, error) {
	for _, key := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy"} {
		value, ok := os.LookupEnv(key)
		if !ok || !safeProxyValue(value) {
			continue
		}
		parsed, err := url.Parse(value)
		if err == nil && parsed.User == nil {
			return parsed, nil
		}
	}
	return nil, nil
}

func (s *Store) savePendingDeviceFlow() error {
	if s.authDeviceCode == "" {
		return nil
	}
	value := map[string]any{
		"device_code":      s.authDeviceCode,
		"expires_at":       s.authDeviceExpiresAt.UnixNano(),
		"interval":         s.authDeviceInterval,
		"verification_uri": s.authDeviceInfo["verification_uri"],
		"user_code":        s.authDeviceInfo["user_code"],
	}
	if !s.authDeviceLastPoll.IsZero() {
		value["last_poll_at"] = s.authDeviceLastPoll.UnixNano()
	}
	return s.savePrivateJSON(s.authPendingPath, value)
}

func (s *Store) restorePendingDeviceFlow() bool {
	if s.authDeviceCode != "" {
		return true
	}
	value, err := s.loadJSON(s.authPendingPath, 16*1024)
	if err != nil {
		return false
	}
	pending, ok := value.(map[string]any)
	if !ok {
		return false
	}
	deviceCode, ok := pending["device_code"].(string)
	userCode := responseString(pending, "user_code")
	verification := responseString(pending, "verification_uri")
	expiresAt, expiresOK := responseTime(pending["expires_at"])
	interval, intervalOK := responseInt(pending, "interval", 5)
	lastPoll, _ := responseTime(pending["last_poll_at"])
	parsed, parseErr := url.Parse(verification)
	if !ok || !validDeviceCode(deviceCode) || !userCodeRE.MatchString(userCode) || parseErr != nil || parsed.Scheme != "https" || parsed.Host != "github.com" || parsed.Path != "/login/device" || !expiresOK || !expiresAt.After(time.Now()) || !intervalOK || interval < 1 || interval > 60 {
		s.clearDeviceFlow()
		return false
	}
	s.authDeviceCode = deviceCode
	s.authDeviceExpiresAt = expiresAt
	s.authDeviceInterval = interval
	s.authDeviceLastPoll = lastPoll
	s.authDeviceInfo = map[string]string{
		"verification_uri": "https://github.com/login/device",
		"user_code":        strings.ToUpper(userCode),
	}
	return true
}

func validDeviceCode(value string) bool {
	return len(value) <= 1024 && deviceCodeRE.MatchString(value)
}

func (s *Store) clearDeviceFlow() {
	s.authDeviceCode = ""
	s.authDeviceExpiresAt = time.Time{}
	s.authDeviceInterval = 5
	s.authDeviceLastPoll = time.Time{}
	s.authDeviceInfo = map[string]string{}
	_ = os.Remove(s.authPendingPath)
}

func (s *Store) authResponse(status, message string) map[string]any {
	response := map[string]any{"status": status}
	if message != "" {
		response["message"] = message
	}
	for _, key := range []string{"verification_uri", "user_code"} {
		if value := s.authDeviceInfo[key]; value != "" {
			response[key] = value
		}
	}
	if s.authDeviceCode != "" {
		remaining := int(time.Until(s.authDeviceExpiresAt).Seconds())
		if remaining < 0 {
			remaining = 0
		}
		response["expires_in"] = remaining
		response["interval"] = s.authDeviceInterval
	}
	return response
}

func (s *Store) ghAuthenticated() bool {
	gh, err := s.executable("gh")
	if err != nil {
		return false
	}
	result, runErr := s.runCommand([]string{gh, "auth", "status", "--hostname", "github.com"}, s.dataDir, "", 30*time.Second, false, nil)
	return runErr == nil && result.code == 0
}

// GitHubRepositories lists the repositories visible to the stored GitHub
// token so the Web UI can offer a selector instead of making the owner and
// name mandatory text fields. The token never leaves this package.
func (s *Store) GitHubRepositories() (map[string]any, error) {
	token, err := s.githubToken()
	if err != nil {
		return nil, err
	}
	repositories, truncated, err := s.fetchGitHubRepositories(token)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"repositories": repositories,
		"count":        len(repositories),
		"truncated":    truncated,
	}, nil
}

func (s *Store) githubToken() (string, error) {
	gh, err := s.executable("gh")
	if err != nil {
		return "", errStore("GitHub authorization is unavailable")
	}
	result, runErr := s.runCommand([]string{gh, "auth", "token", "--hostname", "github.com"}, s.dataDir, "", 20*time.Second, false, nil)
	if runErr != nil || result.code != 0 || result.truncated {
		return "", errStore("GitHub authorization is unavailable")
	}
	token := strings.TrimSpace(result.stdout)
	if !githubTokenRE.MatchString(token) {
		return "", errStore("GitHub authorization is unavailable")
	}
	return token, nil
}

const (
	githubRepositoriesPerPage = 100
	maxGitHubRepositories     = 200
	maxGitHubRepositoryPages  = 2
	maxGitHubAPIPayload       = 2 * 1024 * 1024
)

type githubRepository struct {
	FullName      string `json:"full_name"`
	Private       bool   `json:"private"`
	Archived      bool   `json:"archived"`
	DefaultBranch string `json:"default_branch"`
}

func (s *Store) fetchGitHubRepositories(token string) ([]map[string]any, bool, error) {
	base := strings.TrimRight(s.githubAPIBase, "/")
	if base == "" {
		base = githubAPIURL
	}
	seen := make(map[string]struct{})
	repositories := make([]map[string]any, 0, 32)
	truncated := false
	for page := 1; page <= maxGitHubRepositoryPages; page++ {
		endpoint, err := url.Parse(base + "/user/repos")
		if err != nil {
			return nil, false, errStore("GitHub repository list is unavailable")
		}
		query := endpoint.Query()
		query.Set("per_page", strconv.Itoa(githubRepositoriesPerPage))
		query.Set("page", strconv.Itoa(page))
		query.Set("sort", "updated")
		query.Set("affiliation", "owner,collaborator,organization_member")
		endpoint.RawQuery = query.Encode()

		raw, err := s.githubAPIGet(endpoint.String(), token)
		if err != nil {
			return nil, false, err
		}
		var items []githubRepository
		if err := json.Unmarshal(raw, &items); err != nil {
			return nil, false, errStore("GitHub repository list is invalid")
		}
		for _, item := range items {
			owner, name, err := s.validateFullName(item.FullName)
			if err != nil {
				continue
			}
			fullName := owner + "/" + name
			if _, ok := seen[fullName]; ok {
				continue
			}
			if len(repositories) >= maxGitHubRepositories {
				truncated = true
				break
			}
			seen[fullName] = struct{}{}
			entry := map[string]any{
				"full_name": fullName,
				"private":   item.Private,
				"archived":  item.Archived,
			}
			if branch := publicText(item.DefaultBranch, 200); branch != "" && branchRE.MatchString(branch) {
				entry["default_branch"] = branch
			}
			repositories = append(repositories, entry)
		}
		if truncated || len(items) < githubRepositoriesPerPage {
			break
		}
		if page == maxGitHubRepositoryPages {
			truncated = true
		}
	}
	return repositories, truncated, nil
}

func (s *Store) githubAPIGet(endpoint, token string) ([]byte, error) {
	request, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, errStore("GitHub repository list is unavailable")
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("User-Agent", "folio-service/1")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			Proxy: safeProxyFunc,
		},
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, errStore("GitHub repository list is unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, errStore("GitHub repository list is unavailable")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxGitHubAPIPayload+1))
	if err != nil || len(raw) > maxGitHubAPIPayload {
		return nil, errStore("GitHub repository list is invalid")
	}
	return raw, nil
}

func responseString(value map[string]any, key string) string {
	if text, ok := value[key].(string); ok {
		return text
	}
	return ""
}

func responseInt(value map[string]any, key string, fallback int) (int, bool) {
	raw, ok := value[key]
	if !ok {
		return fallback, true
	}
	switch number := raw.(type) {
	case float64:
		return int(number), number == float64(int(number))
	case json.Number:
		parsed, err := strconv.Atoi(string(number))
		return parsed, err == nil
	case string:
		parsed, err := strconv.Atoi(number)
		return parsed, err == nil
	default:
		return 0, false
	}
}

func responseTime(value any) (time.Time, bool) {
	switch number := value.(type) {
	case float64:
		// The previous Python store persisted epoch seconds as a float. The Go
		// store uses nanoseconds to preserve sub-second poll state without
		// changing the file layout or exposing the device code.
		if number > -1e12 && number < 1e12 {
			seconds := int64(number)
			nanos := int64((number - float64(seconds)) * 1e9)
			return time.Unix(seconds, nanos).UTC(), true
		}
		return time.Unix(0, int64(number)).UTC(), true
	case json.Number:
		parsed, err := strconv.ParseInt(string(number), 10, 64)
		if err == nil {
			return time.Unix(0, parsed).UTC(), true
		}
	case string:
		if parsed, err := time.Parse(time.RFC3339Nano, number); err == nil {
			return parsed, true
		}
		if parsed, err := strconv.ParseInt(number, 10, 64); err == nil {
			if parsed > -1e12 && parsed < 1e12 {
				return time.Unix(parsed, 0).UTC(), true
			}
			return time.Unix(0, parsed).UTC(), true
		}
	}
	return time.Time{}, false
}
