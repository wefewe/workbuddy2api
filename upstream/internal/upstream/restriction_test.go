package upstream

import "testing"

// 11140 + 「账号存在异常行为」话术 → 账号级风控限制（可申诉），不是凭证失效。
func TestIsUpstreamRestriction(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{
			name: "CN 风控限制（中文话术）",
			body: `{"code":11140,"msg":"request illegal","displayMsg":{"zh":"账号存在异常行为，已被限制使用"},"displayTips":{"zh":"系统检测到该账号存在异常使用行为，如需申诉请提交反馈"}}`,
			want: true,
		},
		{
			name: "英文 unusual activity",
			body: `{"code":11140,"msg":"request illegal","displayMsg":{"en":"Your account has been restricted due to unusual activity"}}`,
			want: true,
		},
		{
			name: "繁体 異常行為",
			body: `{"code":11140,"msg":"request illegal","displayMsg":{"zh-hant":"帳號存在異常行為，已被限制使用"}}`,
			want: true,
		},
		{
			name: "11140 但无风控话术（历史 auth_forbidden）→ 仍按原策略",
			body: `{"code":11140,"msg":"request illegal"}`,
			want: false,
		},
		{
			name: "11-128 渠道校验不算风控限制",
			body: `{"code":11-128,"msg":"Illegal API invocation from an unapproved channel","displayMsg":{"zh":"账号存在异常行为"}}`,
			want: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsUpstreamRestriction(c.body); got != c.want {
				t.Fatalf("IsUpstreamRestriction=%v want %v", got, c.want)
			}
		})
	}
}
