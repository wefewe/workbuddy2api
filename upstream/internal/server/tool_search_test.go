package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"workbuddy2api/internal/auth"
	"workbuddy2api/internal/upstream"
)

// searchRecorder 记录 agenttool 出站请求体（0057 测试专用，与 newFakeUpstream
// 的 roundTripFunc 管线同构：不联网、按调用序返回）。
type searchRecorder struct {
	mu     sync.Mutex
	bodies []map[string]any
	seq    []string // 每次上游调用应返回的原始 body
	calls  int
}

func newSearchUpstream(t *testing.T, rec *searchRecorder, responses ...string) *upstream.Client {
	t.Helper()
	rec.seq = responses
	return &upstream.Client{
		HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			rec.mu.Lock()
			idx := rec.calls
			rec.calls++
			rec.mu.Unlock()
			var in map[string]any
			_ = json.NewDecoder(r.Body).Decode(&in)
			rec.mu.Lock()
			rec.bodies = append(rec.bodies, in)
			body := rec.seq[min(idx, len(rec.seq)-1)]
			rec.mu.Unlock()
			return &http.Response{
				StatusCode: 200,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(body)),
			}, nil
		})},
		ChatBaseCN:            "https://fake.example",
		BillingBaseCN:         "https://fake.example",
		AgentToolBaseOverride: "https://fake.example",
	}
}

func (rec *searchRecorder) requestBodies() []map[string]any {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return append([]map[string]any(nil), rec.bodies...)
}

func (rec *searchRecorder) callCount() int {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return rec.calls
}

// newSearchHandler 单账号池（或 caller 传入多账号）的最小 handler 装配。
func newSearchHandler(up *upstream.Client, auths ...*auth.Auth) *Handler {
	cfg := Config{
		Pool:      testPoolWith(auths...),
		Upstream:  up,
		MaxRotate: 3,
	}
	return NewHandler(cfg)
}

func searchReq(query string) *http.Request {
	return httptest.NewRequest("POST", "/v1/tools/search", strings.NewReader(query))
}

const searchOK = `{"code":0,"msg":"","results":[{"title":"t","url":"https://a.example/x","snippet":"s"}],"total_results":1,"response_time_ms":12,"provider":"bocha"}`

func TestToolSearchRequiresAuth(t *testing.T) {
	up := &upstream.Client{AgentToolBaseOverride: "https://fake.example"}
	h := newSearchHandler(up)
	h.cfg.APIKey = "k1"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, searchReq(`{"query":"x"}`))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("want 401, got %d", rec.Code)
	}
}

func TestToolSearchEmptyQuery(t *testing.T) {
	h := newSearchHandler(&upstream.Client{})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, searchReq(`{"query":"  "}`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}

func TestToolSearchBadFreshness(t *testing.T) {
	h := newSearchHandler(&upstream.Client{})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, searchReq(`{"query":"x","freshness":"forever"}`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("want 400, got %d", rec.Code)
	}
}

func TestToolSearchBlockedDomainsAppended(t *testing.T) {
	rec := &searchRecorder{}
	up := newSearchUpstream(t, rec, searchOK)
	h := newSearchHandler(up, &auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999})
	res := httptest.NewRecorder()
	h.ServeHTTP(res, searchReq(`{"query":"go generics","blocked_domains":["reddit.com"],"max_results":3}`))
	if res.Code != http.StatusOK {
		t.Fatalf("want 200, got %d body=%s", res.Code, res.Body.String())
	}
	bodies := rec.requestBodies()
	if len(bodies) != 1 {
		t.Fatalf("want 1 upstream call, got %d", len(bodies))
	}
	if q, _ := bodies[0]["query"].(string); !strings.Contains(q, "-site:reddit.com") {
		t.Fatalf("blocked domain not appended: %q", q)
	}
	if mr, _ := bodies[0]["max_results"].(float64); int(mr) != 3 {
		t.Fatalf("max_results not passed: %v", bodies[0]["max_results"])
	}
	if tp, _ := bodies[0]["type"].(string); tp != "text2text" {
		t.Fatalf("type not text2text: %v", bodies[0]["type"])
	}
}

func TestToolSearchRotatesOnQuota15001(t *testing.T) {
	rec := &searchRecorder{}
	up := newSearchUpstream(t, rec,
		`{"code":15001,"msg":"quota_web_search"}`,
		searchOK)
	h := newSearchHandler(up,
		&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999},
		&auth.Auth{UID: "u2", AccessToken: "at2", ExpiresAt: 9999999999})
	res := httptest.NewRecorder()
	h.ServeHTTP(res, searchReq(`{"query":"q"}`))
	if res.Code != http.StatusOK {
		t.Fatalf("want 200 after rotate, got %d body=%s", res.Code, res.Body.String())
	}
	if rec.callCount() != 2 {
		t.Fatalf("want 2 upstream calls (rotate), got %d", rec.callCount())
	}
	var out struct {
		Code    int `json:"code"`
		Results []struct {
			URL string `json:"url"`
		} `json:"results"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Code != 0 || len(out.Results) != 1 {
		t.Fatalf("want code=0 with 1 result, got %+v", out)
	}
}

func TestToolSearchQuotaAllExhausted429(t *testing.T) {
	rec := &searchRecorder{}
	up := newSearchUpstream(t, rec, `{"code":15001,"msg":"quota_web_search"}`)
	h := newSearchHandler(up,
		&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999},
		&auth.Auth{UID: "u2", AccessToken: "at2", ExpiresAt: 9999999999})
	res := httptest.NewRecorder()
	h.ServeHTTP(res, searchReq(`{"query":"q"}`))
	if res.Code != http.StatusTooManyRequests {
		t.Fatalf("want 429, got %d body=%s", res.Code, res.Body.String())
	}
	var out map[string]any
	if err := json.Unmarshal(res.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out["code"] != "quota_web_search" {
		t.Fatalf("want quota_web_search, got %v", out["code"])
	}
	// MaxRotate=3：两个号都冷却后第三轮 pick nil 提前终止 → 上游最多 2 次调用。
	if rec.callCount() != 2 {
		t.Fatalf("want 2 upstream calls, got %d", rec.callCount())
	}
}

func TestToolSearchQuotaCooldownApplied(t *testing.T) {
	rec := &searchRecorder{}
	up := newSearchUpstream(t, rec, `{"code":15001,"msg":"quota_web_search"}`)
	h := newSearchHandler(up, &auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999})
	res := httptest.NewRecorder()
	h.ServeHTTP(res, searchReq(`{"query":"q"}`))
	if res.Code != http.StatusTooManyRequests {
		t.Fatalf("want 429, got %d", res.Code)
	}
	// 单号 15001 → 固定软冷却：该号从可用集合消失。
	// （pick 在全池冷却时会走 fallback_earliest_expiry 兜底——既有池语义，
	// 测试断言用「可用账号数」而不是 pick 是否 nil。）
	if n := len(h.cfg.Pool.AvailableUIDsForRealm("")); n != 0 {
		t.Fatalf("u1 should be cooled down after 15001, but %d account(s) available", n)
	}
}

func TestToolSearchGlobalAccountSearched(t *testing.T) {
	rec := &searchRecorder{}
	up := newSearchUpstream(t, rec, searchOK)
	// global realm 已支持：agenttool 域按账号 realm 分域（global → workbuddy.ai，
	// 两域端点均已探针验证存在）。global 号应正常搜索。
	h := newSearchHandler(up, &auth.Auth{UID: "g1", AccessToken: "at1", ExpiresAt: 9999999999, Domain: "www.workbuddy.ai"})
	res := httptest.NewRecorder()
	h.ServeHTTP(res, searchReq(`{"query":"q"}`))
	if res.Code != http.StatusOK {
		t.Fatalf("want 200 for global account, got %d body=%s", res.Code, res.Body.String())
	}
	if rec.callCount() != 1 {
		t.Fatalf("want 1 upstream call, got %d", rec.callCount())
	}
}

func TestToolSearchNoAccounts503(t *testing.T) {
	h := newSearchHandler(newSearchUpstream(t, &searchRecorder{}, searchOK))
	res := httptest.NewRecorder()
	h.ServeHTTP(res, searchReq(`{"query":"q"}`))
	if res.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503, got %d", res.Code)
	}
}
