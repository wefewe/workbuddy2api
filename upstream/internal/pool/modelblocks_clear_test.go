package pool

import (
	"testing"
	"time"

	"workbuddy2api/internal/auth"
)

// ClearModelBlocks 只清 11102 负缓存，不碰 6004 的独立冷却表。
func TestClearModelBlocks(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1"})
	p.Add(&auth.Auth{UID: "u2"})

	now := time.Now()
	block := func() modelCooldown {
		return modelCooldown{Until: now.Add(6 * time.Hour), Reason: "11102 model not available"}
	}
	p.mu.Lock()
	p.byUID["u1"].modelCooldowns = map[string]modelCooldown{
		"glm-5.3":             block(),
		"deepseek-v4.1-flash": block(),
	}
	p.byUID["u2"].modelCooldowns = map[string]modelCooldown{
		"deepseek-v4.1-flash": block(),
		// 6004 是配额重置语义，不能被本方法顺带清掉
		"glm-5.3": {Until: now.Add(time.Hour), ResetAt: now.Add(time.Hour), Reason: "6004"},
	}
	p.mu.Unlock()

	if n := p.ClearModelBlocks("deepseek-v4.1-flash"); n != 2 {
		t.Fatalf("ClearModelBlocks = %d, want 2", n)
	}
	if n := p.ClearModelBlocks("deepseek-v4.1-flash"); n != 0 {
		t.Fatalf("重复清理 = %d, want 0", n)
	}
	if n := p.ClearModelBlocks("glm-5.3"); n != 1 {
		t.Fatalf("glm-5.3 清理 = %d, want 1（只清 u1 的 11102）", n)
	}
	p.mu.RLock()
	_, kept := p.byUID["u2"].modelCooldowns["glm-5.3"]
	p.mu.RUnlock()
	if !kept {
		t.Fatal("u2 的 6004 冷却不应被清掉")
	}
	if n := p.ClearModelBlocks(""); n != 0 {
		t.Fatalf("空模型名 = %d, want 0", n)
	}
}
