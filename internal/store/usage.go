package store

import (
	"path/filepath"
	"sort"
	"time"
)

// usageBucket is one aggregation cell: either a calendar day or a
// runner/model pair.
type usageBucket struct {
	Input    int64 `json:"input_tokens"`
	Cached   int64 `json:"cached_tokens"`
	Output   int64 `json:"output_tokens"`
	Total    int64 `json:"total_tokens"`
	Runs     int   `json:"runs"`
	Reported int   `json:"reported_runs"`
}

func (b *usageBucket) add(usage map[string]any) {
	b.Runs++
	if usage == nil {
		return
	}
	if responseString(usage, "source") != "reported" {
		return
	}
	input, _ := usage["input_tokens"].(float64)
	cached, _ := usage["cached_tokens"].(float64)
	output, _ := usage["output_tokens"].(float64)
	total, _ := usage["total_tokens"].(float64)
	if input < 0 || cached < 0 || output < 0 || total < 0 {
		return
	}
	b.Input += int64(input)
	b.Cached += int64(cached)
	b.Output += int64(output)
	b.Total += int64(total)
	b.Reported++
}

// UsageSummary aggregates the token usage runners reported over a window of
// calendar days. It reads immutable run records only, so a long model call
// cannot block the dashboard.
//
// A run that never called a model has no usage at all; it is counted in Runs
// but not in Reported. The caller is expected to show that difference instead
// of pretending the missing tokens were zero.
func (s *Store) UsageSummary(days int) (map[string]any, error) {
	if days < 1 {
		days = 30
	}
	if days > 365 {
		days = 365
	}
	files, err := s.automationRunFiles()
	if err != nil {
		return nil, err
	}
	now := time.Now()
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	start := today.AddDate(0, 0, -(days - 1))

	byDay := map[string]*usageBucket{}
	byModel := map[string]*usageBucket{}
	modelNames := map[string][2]string{}
	totals := &usageBucket{}
	for _, name := range files {
		value, loadErr := s.loadJSON(filepath.Join(s.runsDir, name), maxAutoRunBytes)
		if loadErr != nil {
			continue
		}
		run, ok := value.(map[string]any)
		if !ok {
			continue
		}
		started, parseErr := time.Parse(time.RFC3339Nano, responseString(run, "started_at"))
		if parseErr != nil || started.Before(start) {
			continue
		}
		usage, _ := run["usage"].(map[string]any)
		day := started.Local().Format("2006-01-02")
		bucket := byDay[day]
		if bucket == nil {
			bucket = &usageBucket{}
			byDay[day] = bucket
		}
		bucket.add(usage)
		totals.add(usage)

		// The model table only lists runners that actually reported tokens; a
		// skipped run has no model to attribute and would add a zero row.
		if usage != nil && responseString(usage, "source") == "reported" {
			runner := responseString(run, "runner")
			model := responseString(run, "model")
			key := runner + "/" + model
			entry := byModel[key]
			if entry == nil {
				entry = &usageBucket{}
				byModel[key] = entry
				modelNames[key] = [2]string{runner, model}
			}
			entry.add(usage)
		}
	}

	dayKeys := make([]string, 0, len(byDay))
	for key := range byDay {
		dayKeys = append(dayKeys, key)
	}
	sort.Strings(dayKeys)
	daysOut := make([]any, 0, len(dayKeys))
	for _, key := range dayKeys {
		bucket := byDay[key]
		daysOut = append(daysOut, map[string]any{
			"date": key, "input_tokens": bucket.Input, "cached_tokens": bucket.Cached,
			"output_tokens": bucket.Output, "total_tokens": bucket.Total,
			"runs": bucket.Runs, "reported_runs": bucket.Reported,
		})
	}

	modelKeys := make([]string, 0, len(byModel))
	for key := range byModel {
		modelKeys = append(modelKeys, key)
	}
	sort.Slice(modelKeys, func(i, j int) bool {
		left, right := byModel[modelKeys[i]], byModel[modelKeys[j]]
		if left.Total != right.Total {
			return left.Total > right.Total
		}
		return modelKeys[i] < modelKeys[j]
	})
	modelsOut := make([]any, 0, len(modelKeys))
	for _, key := range modelKeys {
		bucket := byModel[key]
		names := modelNames[key]
		modelsOut = append(modelsOut, map[string]any{
			"runner": names[0], "model": names[1],
			"input_tokens": bucket.Input, "cached_tokens": bucket.Cached,
			"output_tokens": bucket.Output, "total_tokens": bucket.Total,
			"runs": bucket.Runs, "reported_runs": bucket.Reported,
		})
	}

	return map[string]any{
		"window_days":  days,
		"started_on":   start.Format("2006-01-02"),
		"generated_at": time.Now().UTC().Format(time.RFC3339),
		"totals": map[string]any{
			"input_tokens": totals.Input, "cached_tokens": totals.Cached,
			"output_tokens": totals.Output, "total_tokens": totals.Total,
			"runs": totals.Runs, "reported_runs": totals.Reported,
			"unreported_runs": totals.Runs - totals.Reported,
		},
		"days":   daysOut,
		"models": modelsOut,
	}, nil
}
