package upstream

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Refresh backoff has its own small, bounded retry policy. It must not share
// model quota parsing or infer a permanent restriction from HTTP 429 alone.
func refreshRetryAfter(header http.Header, raw []byte) time.Duration {
	seconds := float64(300)
	value := strings.TrimSpace(header.Get("Retry-After"))
	if n, err := strconv.ParseFloat(value, 64); err == nil {
		seconds = n
	} else if at, err := http.ParseTime(value); err == nil {
		seconds = time.Until(at).Seconds()
	} else {
		var body struct {
			RetryAfter float64 `json:"retry_after"`
			Policy     struct {
				RetryAfter float64 `json:"retry_after"`
			} `json:"retry_policy"`
		}
		if json.Unmarshal(raw, &body) == nil {
			if body.RetryAfter != 0 {
				seconds = body.RetryAfter
			} else if body.Policy.RetryAfter != 0 {
				seconds = body.Policy.RetryAfter
			}
		}
	}
	if seconds < 300 {
		seconds = 300
	}
	if seconds > 21600 {
		seconds = 21600
	}
	return time.Duration(seconds) * time.Second
}
