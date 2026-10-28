package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"workbuddy2api/internal/auth"
	"workbuddy2api/internal/upstream"
)

func TestChannelRejectionIsRequestScoped(t *testing.T) {
	for _, consoleMissing := range []bool{true, false} {
		t.Run(map[bool]string{true: "console-missing", false: "both-rejected"}[consoleMissing], func(t *testing.T) {
			accounts := map[string]bool{}
			calls := 0
			up := &upstream.Client{HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				accounts[r.Header.Get("X-User-Id")] = true
				if consoleMissing && strings.Contains(r.URL.Path, "/console/") {
					return sseResponse(400, `{"code":11102,"msg":"model [deepseek-v4.1-flash] service info not found"}`), nil
				}
				return sseResponse(400, channelCheckBody()), nil
			})}, ChatBaseCN: "https://test.invalid"}
			p := testPoolWith(&auth.Auth{UID: "u1", AccessToken: "a", ExpiresAt: 9999999999},
				&auth.Auth{UID: "u2", AccessToken: "b", ExpiresAt: 9999999999})
			h := NewHandler(Config{Pool: p, Upstream: up})
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(
				`{"model":"deepseek-v4.1-flash","stream":true,"messages":[{"role":"user","content":"hi"}]}`)))
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "11128") {
				t.Fatalf("must expose original channel rejection: status=%d body=%s", rec.Code, rec.Body.String())
			}
			if len(accounts) != 1 || calls > 3 {
				t.Fatalf("must not rotate the rejected request across accounts: accounts=%v calls=%d", accounts, calls)
			}
			for _, st := range p.List() {
				if st.Cooling || st.Disabled || st.ConsecutiveFails != 0 || len(st.RateLimitedModels) != 0 || st.InFlight != 0 {
					t.Fatalf("request rejection must not mutate account availability: %+v", st)
				}
			}
		})
	}
}
