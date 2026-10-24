package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const (
	maxConfigBytes   = 256 * 1024
	defaultInterval  = 5 * time.Minute
	minInterval      = time.Minute
	defaultTimeout   = 15 * time.Second
	maxReposPerSourc = 20
)

var (
	hookIDRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,47}$`)
	repoRE   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,99}/[A-Za-z0-9][A-Za-z0-9_.-]{0,99}$`)
	sourceRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
)

type config struct {
	Folio           folioConfig    `json:"folio"`
	StateFile       string         `json:"state_file"`
	IntervalSeconds int            `json:"interval_seconds"`
	Sources         []sourceConfig `json:"sources"`
}

type folioConfig struct {
	URL            string `json:"url"`
	HookID         string `json:"hook_id"`
	SecretFile     string `json:"secret_file"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

type sourceConfig struct {
	Type       string   `json:"type"`
	Name       string   `json:"name"`
	Repos      []string `json:"repos"`
	TokenFile  string   `json:"token_file"`
	IncludePRs bool     `json:"include_prs"`
	Labels     []string `json:"labels"`
}

func loadConfig(path string) (config, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxConfigBytes {
		return config{}, errors.New("config file is missing or invalid")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return config{}, errors.New("config file is unreadable")
	}
	var value config
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return config{}, fmt.Errorf("config is invalid: %w", err)
	}
	applyEnv(&value)
	if value.StateFile == "" {
		value.StateFile = filepath.Join(filepath.Dir(path), "bridge-state.json")
	}
	if err := value.validate(); err != nil {
		return config{}, err
	}
	return value, nil
}

// applyEnv lets a container deployment override the file without editing it.
// Secrets are read from the environment only here; they are never written back.
func applyEnv(value *config) {
	if raw := strings.TrimSpace(os.Getenv("FOLIO_URL")); raw != "" {
		value.Folio.URL = raw
	}
	if raw := strings.TrimSpace(os.Getenv("FOLIO_HOOK_ID")); raw != "" {
		value.Folio.HookID = raw
	}
	if raw := strings.TrimSpace(os.Getenv("FOLIO_HOOK_SECRET")); raw != "" {
		value.Folio.SecretFile = ""
	}
}

func (value *config) validate() error {
	parsed, err := url.Parse(value.Folio.URL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || parsed.User != nil {
		return errors.New("folio.url must be an http(s) URL without credentials")
	}
	if !hookIDRE.MatchString(value.Folio.HookID) {
		return errors.New("folio.hook_id is invalid")
	}
	if os.Getenv("FOLIO_HOOK_SECRET") == "" && value.Folio.SecretFile == "" {
		return errors.New("folio.secret_file or FOLIO_HOOK_SECRET is required")
	}
	if value.IntervalSeconds == 0 {
		value.IntervalSeconds = int(defaultInterval / time.Second)
	}
	if time.Duration(value.IntervalSeconds)*time.Second < minInterval {
		return fmt.Errorf("interval_seconds must be at least %d", int(minInterval/time.Second))
	}
	if value.Folio.TimeoutSeconds <= 0 || value.Folio.TimeoutSeconds > 120 {
		value.Folio.TimeoutSeconds = int(defaultTimeout / time.Second)
	}
	if len(value.Sources) == 0 || len(value.Sources) > 8 {
		return errors.New("one to eight sources are required")
	}
	seen := map[string]bool{}
	for index, source := range value.Sources {
		if source.Type != "github" {
			return fmt.Errorf("source %d has an unsupported type %q", index, source.Type)
		}
		if !sourceRE.MatchString(source.Name) {
			return fmt.Errorf("source %d needs a lowercase name", index)
		}
		if seen[source.Name] {
			return fmt.Errorf("source name %q is duplicated", source.Name)
		}
		seen[source.Name] = true
		if len(source.Repos) == 0 || len(source.Repos) > maxReposPerSourc {
			return fmt.Errorf("source %q needs one to %d repositories", source.Name, maxReposPerSourc)
		}
		if os.Getenv("GITHUB_TOKEN") == "" && source.TokenFile == "" {
			return fmt.Errorf("source %q needs a token_file or GITHUB_TOKEN", source.Name)
		}
		for _, repo := range source.Repos {
			if !repoRE.MatchString(repo) || strings.Contains(repo, "..") {
				return fmt.Errorf("source %q has an invalid repository %q", source.Name, repo)
			}
		}
	}
	return nil
}
