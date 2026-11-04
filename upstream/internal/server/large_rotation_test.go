package server

import (
	"testing"
	"time"
)

// 默认：预算模式（6 条 / 120s 窗口），阈值 2 MiB。单次大请求不触发任何排除。
func TestLargeBudgetDefaults(t *testing.T) {
	h := NewHandler(Config{})
	if h.cfg.LargeRequestBytes != 2<<20 {
		t.Fatalf("默认阈值 = %d, want 2097152", h.cfg.LargeRequestBytes)
	}
	if h.cfg.LargeRequestBudget != 6 {
		t.Fatalf("默认预算 = %d, want 6", h.cfg.LargeRequestBudget)
	}
	if h.cfg.LargeRequestBudgetWindow != 120*time.Second {
		t.Fatalf("默认窗口 = %v, want 120s", h.cfg.LargeRequestBudgetWindow)
	}
	if h.cfg.LargeRequestCooldown != 0 {
		t.Fatalf("默认 cooldown = %v, want 0（legacy 轮换关闭）", h.cfg.LargeRequestCooldown)
	}
	now := time.Now()
	h.noteLargeUse("u1", now)
	if got := h.largeExclusions(now); got["u1"] {
		t.Fatalf("预算还有余量时不应排除, got %v", got)
	}
}

// 预算用满后才让出；窗口滑过后自动恢复，并清理过期记录。
func TestLargeBudgetExhaustion(t *testing.T) {
	h := NewHandler(Config{}) // 预算 6 / 窗口 120s
	now := time.Now()
	for i := 0; i < 5; i++ {
		h.noteLargeUse("u1", now)
	}
	if got := h.largeExclusions(now); got["u1"] {
		t.Fatalf("5 条未满预算，不应排除, got %v", got)
	}
	h.noteLargeUse("u1", now) // 第 6 条
	if got := h.largeExclusions(now); !got["u1"] {
		t.Fatalf("达到预算应排除, got %v", got)
	}
	// 窗口外：恢复可选中，且过期记录被清理
	if got := h.largeExclusions(now.Add(h.cfg.LargeRequestBudgetWindow + time.Second)); len(got) != 0 {
		t.Fatalf("窗口过后应清空, got %v", got)
	}
	h.largeMu.Lock()
	left := len(h.largeUsed)
	h.largeMu.Unlock()
	if left != 0 {
		t.Fatalf("过期记录未清理, left=%d", left)
	}
}

// legacy 逐请求轮换：显式关掉预算、给 cooldown 后，窗口内用过即排除。
func TestLargeExclusionsWindowLegacyRotation(t *testing.T) {
	h := NewHandler(Config{LargeRequestBudget: -1, LargeRequestCooldown: 90 * time.Second})
	now := time.Now()
	h.noteLargeUse("u1", now)
	if got := h.largeExclusions(now); !got["u1"] {
		t.Fatalf("legacy 轮换下用过的号应排除, got %v", got)
	}
	if got := h.largeExclusions(now); got["u2"] {
		t.Fatalf("未用过的号不应排除, got %v", got)
	}
	if got := h.largeExclusions(now.Add(91 * time.Second)); len(got) != 0 {
		t.Fatalf("窗口过后应清空, got %v", got)
	}
}

// 阈值 <=0 时整条策略关闭。
func TestLargeExclusionsDisabled(t *testing.T) {
	h := NewHandler(Config{LargeRequestBytes: -1})
	now := time.Now()
	h.noteLargeUse("u1", now)
	if got := h.largeExclusions(now); got["u1"] {
		t.Fatalf("策略关闭时不应排除, got %v", got)
	}
}
