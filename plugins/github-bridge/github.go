package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	githubAPIBase    = "https://api.github.com"
	maxIssuesPerPage = 50
	maxAPIBodyBytes  = 1024 * 1024
)

type githubIssue struct {
	Number    int        `json:"number"`
	Title     string     `json:"title"`
	HTMLURL   string     `json:"html_url"`
	State     string     `json:"state"`
	UpdatedAt string     `json:"updated_at"`
	Labels    []apiLabel `json:"labels"`
	Pull      *struct{}  `json:"pull_request"`
}

type apiLabel struct {
	Name string `json:"name"`
}

func (issue githubIssue) isPull() bool { return issue.Pull != nil }

type githubClient struct {
	token  string
	base   string
	client *http.Client
}

func newGitHubClient(token string) *githubClient {
	base := strings.TrimSpace(os.Getenv("GITHUB_API_URL"))
	if base == "" {
		base = githubAPIBase
	}
	return &githubClient{
		token:  token,
		base:   strings.TrimRight(base, "/"),
		client: &http.Client{Timeout: 30 * time.Second, CheckRedirect: refuseRedirect},
	}
}

func readToken(path string) (string, error) {
	raw := strings.TrimSpace(os.Getenv("GITHUB_TOKEN"))
	if raw == "" {
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("github token is unavailable")
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return "", errors.New("github token is unavailable")
		}
		raw = strings.TrimSpace(string(data))
	}
	if len(raw) < 20 || len(raw) > 512 || strings.ContainsAny(raw, " \t\r\n") {
		return "", errors.New("github token looks invalid")
	}
	return raw, nil
}

// listIssues fetches the most recently updated issues. A 304 is reported as
// notModified; a rate limit is reported as a hold so the loop does not hammer
// the API.
func (client *githubClient) listIssues(ctx context.Context, repo, etag string) ([]githubIssue, string, bool, time.Time, error) {
	endpoint, err := url.Parse(client.base + "/repos/" + repo + "/issues")
	if err != nil {
		return nil, "", false, time.Time{}, errors.New("github endpoint is invalid")
	}
	query := endpoint.Query()
	query.Set("state", "all")
	query.Set("sort", "updated")
	query.Set("direction", "desc")
	query.Set("per_page", strconv.Itoa(maxIssuesPerPage))
	endpoint.RawQuery = query.Encode()

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, "", false, time.Time{}, errors.New("github request could not be built")
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	request.Header.Set("User-Agent", "folio-github-bridge")
	request.Header.Set("Authorization", "Bearer "+client.token)
	if etag != "" {
		request.Header.Set("If-None-Match", etag)
	}
	response, err := client.client.Do(request)
	if err != nil {
		return nil, "", false, time.Time{}, errors.New("github is unreachable")
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotModified {
		return nil, etag, true, time.Time{}, nil
	}
	if response.StatusCode == http.StatusForbidden || response.StatusCode == http.StatusTooManyRequests {
		return nil, "", false, rateLimitReset(response), errors.New("github rate limit is active")
	}
	if response.StatusCode != http.StatusOK {
		return nil, "", false, time.Time{}, fmt.Errorf("github returned status %d", response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxAPIBodyBytes))
	if err != nil || len(raw) > maxAPIBodyBytes {
		return nil, "", false, time.Time{}, errors.New("github response is unreadable")
	}
	var issues []githubIssue
	if err := json.Unmarshal(raw, &issues); err != nil {
		return nil, "", false, time.Time{}, errors.New("github response is invalid")
	}
	return issues, response.Header.Get("ETag"), false, time.Time{}, nil
}

func rateLimitReset(response *http.Response) time.Time {
	if raw := response.Header.Get("X-RateLimit-Reset"); raw != "" {
		if seconds, err := strconv.ParseInt(raw, 10, 64); err == nil {
			return time.Unix(seconds, 0).UTC().Add(time.Minute)
		}
	}
	return time.Now().Add(15 * time.Minute)
}
