package contextrepo

import (
	"errors"
	"net"
	"net/url"
	"os"
	"strings"
)

// taskModelConnectionArgs reads only trusted per-invocation connection metadata.
// The store supplies these values in its scrubbed child environment; repository
// packets and prompts cannot select them. Credentials never become CLI args.
func taskModelConnectionArgs() ([]string, string, bool, error) {
	base := os.Getenv("FOLIO_MODEL_BASE_URL")
	model := os.Getenv("FOLIO_MODEL_ID")
	key := os.Getenv("FOLIO_MODEL_KEY")
	if base == "" && model == "" && key == "" {
		return nil, "", false, nil
	}
	invalid := func() ([]string, string, bool, error) {
		return nil, "", true, errors.New("task model connection is invalid")
	}
	if base == "" || len(base) > 2048 || model == "" || len(model) > maxWorkerModelLen || !workerModelPattern.MatchString(model) || key == "" || len(key) > 4096 || strings.ContainsAny(key, "\r\n\x00") {
		return invalid()
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || strings.ContainsAny(base, "\r\n\x00") {
		return invalid()
	}
	if parsed.Scheme != "https" {
		ip := net.ParseIP(parsed.Hostname())
		local := strings.EqualFold(parsed.Hostname(), "localhost") || ip != nil && (ip.IsLoopback() || ip.IsPrivate())
		if parsed.Scheme != "http" || !local {
			return invalid()
		}
	}
	base = strings.TrimRight(base, "/")
	return []string{
		"-c", "model=" + jsonString(model),
		"-c", "model_provider=\"folio_service\"",
		"-c", "model_providers.folio_service.name=\"Folio model service\"",
		"-c", "model_providers.folio_service.base_url=" + jsonString(base),
		"-c", "model_providers.folio_service.env_key=\"FOLIO_MODEL_KEY\"",
		"-c", "model_providers.folio_service.wire_api=\"responses\"",
		"-c", "model_providers.folio_service.requires_openai_auth=false",
	}, "FOLIO_MODEL_KEY", true, nil
}
