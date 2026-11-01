package server

import (
	"strings"
	"testing"

	"workbuddy2api/internal/auth"
	"workbuddy2api/internal/pool"
	"workbuddy2api/internal/upstream"
)

// 11140 + 「账号存在异常行为」= 账号级风控限制 → 软冷却（不 Disable、不清凭证）。
func TestApplyErrorPolicyUpstreamRestrictionCools(t *testing.T) {
	p := pool.New("")
	p.Add(&auth.Auth{UID: "u1"})
	h := NewHandler(Config{Pool: p})

	body := `{"code":11140,"msg":"request illegal","displayMsg":{"zh":"账号存在异常行为，已被限制使用"},"displayTips":{"zh":"如需申诉请提交反馈"}}`
	h.applyErrorPolicy("u1", upstream.ErrAccountFault, body, "glm-5.2", nil)

	st, ok := p.Status("u1")
	if !ok {
		t.Fatal("账号不存在")
	}
	if st.Disabled {
		t.Fatalf("风控限制不应永久禁用: %+v", st)
	}
	if !st.Cooling {
		t.Fatalf("风控限制应进入软冷却: %+v", st)
	}
	if !strings.Contains(st.Reason, "upstream restriction") || !strings.Contains(st.Reason, "appealable") {
		t.Fatalf("冷却理由应说明是上游限制且可申诉, got %q", st.Reason)
	}
	// 冷却时长取 upstreamRestrictionCooldown（45m），明显长于渠道校验的 30s。
	if st.CoolRemaining < int64((30 * 60)) {
		t.Fatalf("冷却时长过短: %d 秒", st.CoolRemaining)
	}
}

// 不带风控话术的 11140 仍按原有策略硬禁用（历史 auth_forbidden 语义不变）。
func TestApplyErrorPolicyPlainRequestIllegalStillDisables(t *testing.T) {
	p := pool.New("")
	p.Add(&auth.Auth{UID: "u1"})
	h := NewHandler(Config{Pool: p})

	h.applyErrorPolicy("u1", upstream.ErrAccountFault, `{"code":11140,"msg":"request illegal"}`, "glm-5.2", nil)
	st, _ := p.Status("u1")
	if !st.Disabled {
		t.Fatalf("无风控话术的 request illegal 应仍禁用: %+v", st)
	}
}
