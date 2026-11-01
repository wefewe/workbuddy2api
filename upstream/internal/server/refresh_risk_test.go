package server

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"workbuddy2api/internal/auth"
)

func TestRefreshRiskAdminAuthAndIndependentRecovery(t *testing.T) {
	p := testPoolWith(&auth.Auth{UID: "u1"})
	for _, enabled := range []bool{false, true} {
		h := NewHandler(Config{Pool: p, APIKey: "key", AdminEnabled: enabled})
		for _, action := range []string{"refresh-risk", "refresh-risk/clear"} {
			path := "/admin/accounts/u1/" + action
			req := httptest.NewRequest("POST", path, strings.NewReader(`{"retry_after_sec":900}`))
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			want := 401
			if !enabled {
				want = 404
			}
			if rec.Code != want {
				t.Fatalf("%s unauth=%d want=%d", action, rec.Code, want)
			}
		}
	}
	h := NewHandler(Config{Pool: p, APIKey: "key", AdminEnabled: true})
	for _, action := range []string{"refresh-risk", "disable", "enable", "refresh-risk/clear"} {
		req := httptest.NewRequest("POST", "/admin/accounts/u1/"+action, strings.NewReader(`{"retry_after_sec":900}`))
		req.Header.Set("Authorization", "Bearer key")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("%s: %s", action, rec.Body.String())
		}
		if action == "enable" && !p.RiskPaused("u1") {
			t.Fatal("enable must not clear independent hold")
		}
	}
	if p.RiskPaused("u1") {
		t.Fatal("dedicated clear failed")
	}
	p.PauseRefreshRateLimited("u1", time.Hour)
	req := httptest.NewRequest("POST", "/admin/accounts/u1/disable", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer key")
	h.ServeHTTP(httptest.NewRecorder(), req)
	req = httptest.NewRequest("POST", "/admin/accounts/u1/refresh-risk/clear", strings.NewReader(`{}`))
	req.Header.Set("Authorization", "Bearer key")
	h.ServeHTTP(httptest.NewRecorder(), req)
	st, _ := p.Status("u1")
	if !st.ManualDisabled || p.RiskPaused("u1") {
		t.Fatal("clearing risk changed operator stop")
	}
}
