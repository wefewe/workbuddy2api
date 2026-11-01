package upstream

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
	"workbuddy2api/internal/auth"
)

func TestRefreshRetryAfterBounded(t *testing.T) {
	for _, tc := range []struct {
		header, body string
		want         time.Duration
	}{{"", "", 5 * time.Minute}, {"1", "", 5 * time.Minute}, {"900", "", 15 * time.Minute}, {"999999999", "", 6 * time.Hour}, {"", `{"retry_after":900}`, 15 * time.Minute}, {"", `{"retry_policy":{"retry_after":1200}}`, 20 * time.Minute}} {
		h := http.Header{}
		h.Set("Retry-After", tc.header)
		if got := refreshRetryAfter(h, []byte(tc.body)); got != tc.want {
			t.Fatalf("%+v: %v", tc, got)
		}
	}
	h := http.Header{}
	h.Set("Retry-After", time.Now().Add(time.Hour).UTC().Format(http.TimeFormat))
	got := refreshRetryAfter(h, nil)
	if got < 59*time.Minute || got > time.Hour {
		t.Fatal(got)
	}
}

func TestRefreshPausedSkipsNetworkAndCallbacks(t *testing.T) {
	c := New()
	c.RefreshPaused = func(uid string) bool { return uid == "u" }
	c.OnRefreshRateLimit = func(string, time.Duration) { t.Fatal("local guard is not a new upstream failure") }
	c.OnRefreshSuccess = func(string) { t.Fatal("blocked refresh cannot count as success") }
	if err := c.RefreshToken(&auth.Auth{UID: "u"}); err == nil {
		t.Fatal("expected local hold")
	}
}

func TestRefreshRateLimitCallbackUsesActualRefreshResponse(t *testing.T) {
	for _, status := range []int{429, 500, 200} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Retry-After", "900")
			w.WriteHeader(status)
			if status == 200 {
				fmt.Fprint(w, `{"code":0,"data":{"accessToken":"new","refreshToken":"new-refresh","expiresIn":3600}}`)
			} else {
				fmt.Fprint(w, `{"code":500,"msg":"temporarily unavailable"}`)
			}
		}))
		c := New()
		c.ChatBaseCN = srv.URL
		limits, successes := 0, 0
		c.OnRefreshRateLimit = func(uid string, d time.Duration) {
			limits++
			if uid != "u" || d != 15*time.Minute {
				t.Errorf("callback uid=%s wait=%v", uid, d)
			}
		}
		c.OnRefreshSuccess = func(string) { successes++ }
		_ = c.RefreshToken(&auth.Auth{UID: "u", AccessToken: "old", RefreshToken: "old-refresh"})
		srv.Close()
		wantLimits, wantSuccesses := 0, 0
		if status == 429 {
			wantLimits = 1
		}
		if status == 200 {
			wantSuccesses = 1
		}
		if limits != wantLimits || successes != wantSuccesses {
			t.Fatalf("status=%d limits=%d successes=%d", status, limits, successes)
		}
	}
}
