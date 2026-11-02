package server

import (
	"strings"
	"testing"

	"workbuddy2api/internal/auth"
	"workbuddy2api/internal/pool"
	"workbuddy2api/internal/upstream"
)

// 11140 + 「账号存在异常行为」= 上游账号封禁 → 明确禁用并写清原因（issue #376）。
//
// 0043 曾把它改成 45 分钟软冷却，但账号会一直在「冷却中」循环，面板看不出
// 哪个号被封、也不会提示申诉。恢复禁用后 #361 的封禁分类与提示才能生效。
func TestApplyErrorPolicyUpstreamRestrictionDisablesAsBan(t *testing.T) {
	p := pool.New("")
	p.Add(&auth.Auth{UID: "u1"})
	h := NewHandler(Config{Pool: p})

	body := `{"code":11140,"msg":"request illegal","displayMsg":{"zh":"账号存在异常行为，已被限制使用"},"displayTips":{"zh":"如需申诉请提交反馈"}}`
	h.applyErrorPolicy("u1", upstream.ErrAccountFault, body, "glm-5.2", nil)

	st, ok := p.Status("u1")
	if !ok {
		t.Fatal("账号不存在")
	}
	if !st.Disabled {
		t.Fatalf("风控限制应禁用账号: %+v", st)
	}
	if st.Cooling {
		t.Fatalf("不应退化成冷却（会一直循环、看不出封禁）: %+v", st)
	}
	for _, want := range []string{"account banned", "账号存在异常行为", "appeal required"} {
		if !strings.Contains(st.DisabledReason, want) {
			t.Fatalf("禁用理由缺少 %q, got %q", want, st.DisabledReason)
		}
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
