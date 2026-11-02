package server

import (
	"testing"
	"time"
)

// 默认关闭：未配置 large_request_cooldown 时既不轮换也不排除任何号。
// 轮换会让上游的前缀缓存整段 miss，长上下文每轮都按全量计费，代价远大于收益。
func TestLargeRotationOffByDefault(t *testing.T) {
	h := NewHandler(Config{})
	if h.cfg.LargeRequestBytes != 256<<10 {
		t.Fatalf("默认阈值 = %d, want 262144", h.cfg.LargeRequestBytes)
	}
	if h.cfg.LargeRequestCooldown != 0 {
		t.Fatalf("默认窗口 = %v, want 0（关闭）", h.cfg.LargeRequestCooldown)
	}
	now := time.Now()
	h.noteLargeUse("u1", now)
	if got := h.largeExclusions(now); len(got) != 0 {
		t.Fatalf("默认关闭时不应排除任何号, got %v", got)
	}
}

// 显式打开后：窗口内同号不再被选中，窗口过后恢复。
func TestLargeExclusionsWindow(t *testing.T) {
	h := NewHandler(Config{LargeRequestCooldown: 90 * time.Second})
	if h.cfg.LargeRequestCooldown != 90*time.Second {
		t.Fatalf("显式窗口 = %v, want 90s", h.cfg.LargeRequestCooldown)
	}

	now := time.Now()
	h.noteLargeUse("u1", now)
	if got := h.largeExclusions(now); !got["u1"] {
		t.Fatalf("刚用过大请求的号应被排除, got %v", got)
	}
	// 别的号不受影响
	if got := h.largeExclusions(now); got["u2"] {
		t.Fatalf("未用过的号不应被排除, got %v", got)
	}
	// 窗口过期后恢复可选中，且过期项被清理
	if got := h.largeExclusions(now.Add(h.cfg.LargeRequestCooldown + time.Second)); len(got) != 0 {
		t.Fatalf("窗口过期后应清空, got %v", got)
	}
	h.largeMu.Lock()
	left := len(h.largeUsed)
	h.largeMu.Unlock()
	if left != 0 {
		t.Fatalf("过期项未清理, left=%d", left)
	}
}

// 关闭策略（<=0）时不做任何排除。
func TestLargeExclusionsDisabled(t *testing.T) {
	h := NewHandler(Config{LargeRequestBytes: -1, LargeRequestCooldown: time.Minute})
	now := time.Now()
	h.noteLargeUse("u1", now)
	if got := h.largeExclusions(now); got["u1"] {
		t.Fatalf("策略关闭时不应排除, got %v", got)
	}
}
