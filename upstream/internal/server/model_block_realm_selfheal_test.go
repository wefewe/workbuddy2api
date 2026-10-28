package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"workbuddy2api/internal/auth"
	"workbuddy2api/internal/upstream"
)

// issue #332：自愈的判据必须与选号**同一个 realm 维度**。
//
// 现场：11 个国内版账号，deepseek-v4.1-flash 在全部账号上被 11102 负缓存，
// 而池里还有其它 realm 的账号（它们不被该模型的负缓存挡）。旧判据
// AvailableUIDsForModel(裸名) 看的是**全池**，于是返回非空 → 自愈不触发 →
// PickExcludingForRealm(realm=cn) 选不出号 → 503；换别的模型一样，因为每个
// cn 模型都会被轮流打成负缓存。
func TestModelBlockSelfHealIsRealmAware(t *testing.T) {
	cn1 := &auth.Auth{UID: "cn1", AccessToken: "a", ExpiresAt: 9999999999, Domain: "www.codebuddy.cn"}
	cn2 := &auth.Auth{UID: "cn2", AccessToken: "b", ExpiresAt: 9999999999, Domain: "www.codebuddy.cn"}
	gl := &auth.Auth{UID: "gl1", AccessToken: "c", ExpiresAt: 9999999999, Domain: "www.workbuddy.ai"}
	p := testPoolWith(cn1, cn2, gl)
	for _, uid := range []string{"cn1", "cn2"} {
		p.BlockModelBackoff(uid, "deepseek-v4.1-flash", "11102 model not available")
	}

	var calls int32
	up := &upstream.Client{HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		atomic.AddInt32(&calls, 1)
		return sseResponse(200, sseOK), nil
	})}, ChatBaseCN: "https://test.invalid"}
	h := NewHandler(Config{Pool: p, Upstream: up})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(
		`{"model":"deepseek-v4.1-flash","stream":true,"messages":[{"role":"user","content":"hi"}]}`)))
	if rec.Code == http.StatusServiceUnavailable {
		t.Fatalf("cn 池被负缓存挡住时必须自愈重试，实际 503：%s", rec.Body.String())
	}
	if atomic.LoadInt32(&calls) == 0 {
		t.Fatal("自愈后应当真的把请求发出去")
	}
	for _, uid := range []string{"cn1", "cn2"} {
		st, ok := p.Status(uid)
		if !ok {
			continue
		}
		if len(st.RateLimitedModels) != 0 {
			t.Fatalf("%s 的 11102 负缓存应已被清掉：%+v", uid, st.RateLimitedModels)
		}
	}
}

// 反向约束：模型**真的**在池里不可用时不能变成重试风暴。
// 自愈只清一次；上游始终 11102 时按错误返回，调用次数有上界。
func TestModelBlockSelfHealIsBounded(t *testing.T) {
	cn1 := &auth.Auth{UID: "cn1", AccessToken: "a", ExpiresAt: 9999999999, Domain: "www.codebuddy.cn"}
	p := testPoolWith(cn1)
	p.BlockModelBackoff("cn1", "deepseek-v4.1-flash", "11102 model not available")

	var calls int32
	up := &upstream.Client{HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		atomic.AddInt32(&calls, 1)
		return sseResponse(400, `{"code":11102,"msg":"model [deepseek-v4.1-flash] service info not found"}`), nil
	})}, ChatBaseCN: "https://test.invalid"}
	h := NewHandler(Config{Pool: p, Upstream: up, MaxRotate: 4})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(
		`{"model":"deepseek-v4.1-flash","stream":true,"messages":[{"role":"user","content":"hi"}]}`)))
	if rec.Code == http.StatusOK {
		t.Fatalf("模型确实不存在时不该报成功：%s", rec.Body.String())
	}
	if n := atomic.LoadInt32(&calls); n > 8 {
		t.Fatalf("自愈不得变成重试风暴，实际上游调用 %d 次", n)
	}
}
