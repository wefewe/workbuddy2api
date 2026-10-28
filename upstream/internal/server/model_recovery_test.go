package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"workbuddy2api/internal/auth"
)

func TestSuccessfulProbeClearsOnlyMissingModel(t *testing.T) {
	p := testPoolWith(&auth.Auth{UID: "u1"})
	p.BlockModelBackoff("u1", "deepseek-v4.1-flash", "11102 model not available")
	p.CooldownSoftForModel("u1", time.Minute, time.Now().Add(time.Hour), "glm-5.3", "6004 model rate limit")
	p.SetManualDisabled("u1", true, "operator")
	h := NewHandler(Config{Pool: p, APIKey: "test", AdminEnabled: true})
	url := "/admin/accounts/u1/models/deepseek-v4.1-flash/available"
	request := httptest.NewRequest("POST", url, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, request)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("recovery requires gateway key: %d", rec.Code)
	}
	request = httptest.NewRequest("POST", url, nil)
	request.Header.Set("Authorization", "Bearer test")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, request)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	st, _ := p.Status("u1")
	if !st.ManualDisabled || len(st.RateLimitedModels) != 1 || st.RateLimitedModels[0].Model != "glm-5.3" {
		t.Fatalf("must preserve real quota and operator state: %+v", st)
	}
}

func TestProbeRecoveryDoesNotClearSameModelQuota(t *testing.T) {
	p := testPoolWith(&auth.Auth{UID: "u1"})
	p.CooldownSoftForModel("u1", time.Minute, time.Now().Add(time.Hour), "deepseek-v4.1-flash", "6004 model rate limit")
	h := NewHandler(Config{Pool: p, AdminEnabled: true})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/admin/accounts/u1/models/deepseek-v4.1-flash/available", nil))
	st, _ := p.Status("u1")
	if rec.Code != http.StatusOK || len(st.RateLimitedModels) != 1 {
		t.Fatalf("successful small probe must not erase token quota: %d %+v", rec.Code, st)
	}
}
