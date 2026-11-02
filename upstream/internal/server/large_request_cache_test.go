package server

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"workbuddy2api/internal/auth"
	"workbuddy2api/internal/session"
)

// largeBodyWith 组装一个超过 large_request_bytes 默认阈值（256 KiB）的请求体。
func largeBodyWith(convID string) string {
	return `{"model":"glm-5.2","messages":[{"role":"user","content":"` +
		strings.Repeat("x", 256<<10) + `"}],"metadata":{"conversation_id":"` + convID + `"}}`
}

// TestChatLargeRequestKeepsStickyByDefault 大请求默认不轮换：成功号要写进会话绑定，
// 下一轮仍在同一个号上，上游的前缀缓存才能整段命中。
func TestChatLargeRequestKeepsStickyByDefault(t *testing.T) {
	st := newBindStore()
	sess := session.New(session.Config{
		TTL:       time.Minute,
		Store:     st,
		Available: func() []string { return []string{"good"} },
	})
	p := testPoolWith(&auth.Auth{UID: "good", AccessToken: "at-good", ExpiresAt: 9999999999})
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		return 200, sseOK, true
	})
	h := NewHandler(Config{Pool: p, Upstream: up, Session: sess, SoftCooldown: time.Minute})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(largeBodyWith("conv-large"))))
	if rec.Code != 200 {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
	if uid, ok := st.lastUID("conv-large"); !ok || uid != "good" {
		t.Fatalf("大请求成功也应写会话绑定, got %s ok=%v", uid, ok)
	}
}

// TestChatLargeRequestRotationDoesNotBind 显式打开轮换后，大请求不写绑定（逐请求换号）。
func TestChatLargeRequestRotationDoesNotBind(t *testing.T) {
	st := newBindStore()
	sess := session.New(session.Config{
		TTL:       time.Minute,
		Store:     st,
		Available: func() []string { return []string{"good"} },
	})
	p := testPoolWith(&auth.Auth{UID: "good", AccessToken: "at-good", ExpiresAt: 9999999999})
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		return 200, sseOK, true
	})
	h := NewHandler(Config{
		Pool:                 p,
		Upstream:             up,
		Session:              sess,
		SoftCooldown:         time.Minute,
		LargeRequestCooldown: 90 * time.Second,
	})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(largeBodyWith("conv-large-rot"))))
	if rec.Code != 200 {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
	if uid, ok := st.lastUID("conv-large-rot"); ok {
		t.Fatalf("轮换开启时大请求不应写绑定, got %s", uid)
	}
}
