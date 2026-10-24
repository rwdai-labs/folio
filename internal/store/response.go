package store

import (
	"encoding/json"
	"strconv"
	"time"
)

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
