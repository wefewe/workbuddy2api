package upstream

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"workbuddy2api/internal/auth"
)

// 复现 2026-10-28 整池负缓存事故的真实触发顺序。
//
// 现场：/v2 撞 11128（渠道校验）后 markChannelBlocked 记 10 分钟，之后 chatPaths 把
// /console 排到首位。这时 /console 报 11102 落在 i == 0，旧判据
// `if i > 0 && kind == ErrModelBlocked` 不成立，11102 被当成「该账号缺这个模型」，
// handler 写 6 小时负缓存；十几个号轮一圈就是整池 503 no_healthy_account。
//
// 修复后的判据按路径走：/console 自己报 11102 一律降级，且还有候选时换回 /v2 继续试。
// 所以这条用例在修复前必须失败（err 非空且 Kind == ErrModelBlocked、只走了一条路径），
// 修复后必须成功返回 200。
func TestChannelBlockedConsoleFirstModelMissingFallsBack(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if strings.Contains(r.URL.Path, "/console/chat/completions") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"code":11102,"msg":"model [deepseek-v4.1-flash] service info not found"}`))
			return
		}
		// /v2 正常放行：修复后应当换回来并成功。
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"}}]}\n\ndata: [DONE]\n\n"))
	}))
	defer srv.Close()

	c := New()
	c.ChatBaseCN = srv.URL
	// 用 global 账号：只有 global 会因渠道校验把 /console 提前（CN 保持 /v2 优先，
	// 见 0032-console-first-global-only）。分类判定本身与 realm 无关，这里覆盖的是
	// 「/console 排首位时 11102 不能被升级成 model_blocked」，所以仍要能构造出该顺序。
	acct := &auth.Auth{UID: "g1", AccessToken: "at", ExpiresAt: 9999999999,
		Domain: "www.workbuddy.ai"}
	// 关键前置：模拟「撞过 11128」，chatPaths 因此返回 [/console, /v2]。
	c.markChannelBlocked("g1")

	body, status, raw, err := c.ChatStream(acct,
		[]byte(`{"model":"deepseek-v4.1-flash","messages":[{"role":"user","content":"hi"}]}`), "", ChatMeta{})
	if err != nil {
		t.Fatalf("旧判据把 /console 首位的 11102 升级成 model_blocked：status=%d err=%v body=%s paths=%v",
			status, err, raw, paths)
	}
	if status != http.StatusOK {
		t.Fatalf("status=%d body=%s paths=%v", status, raw, paths)
	}
	if body != nil {
		body.Close()
	}
	if len(paths) != 2 || !strings.Contains(paths[0], "/console/") || !strings.Contains(paths[1], "/v2/") {
		t.Fatalf("应当先走 /console 再换回 /v2，实际路径=%v", paths)
	}
}
