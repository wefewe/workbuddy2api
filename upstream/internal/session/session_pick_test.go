package session

import (
	"testing"
	"time"
)

// TestResolveForModelUsesPickForModel 注入 PickForModel 后，**首绑必须走权重回调**
// （issue #265）：这是「快过期积分优先消耗」在会话粘性下生效的唯一入口。
func TestResolveForModelUsesPickForModel(t *testing.T) {
	r := routerWith(newCountingStore(), []string{"a1", "a2", "a3"}, time.Minute)
	var gotModel string
	var gotCandidates []string
	r.cfg.PickForModel = func(model string, candidates []string) string {
		gotModel = model
		gotCandidates = append([]string(nil), candidates...)
		return "a3"
	}

	uid, ok := r.ResolveForModel("c1", "cn:deepseek-v4.1-flash")
	if !ok || uid != "a3" {
		t.Fatalf("ResolveForModel=%q ok=%v want a3/true（应采信回调结果）", uid, ok)
	}
	if gotModel != "cn:deepseek-v4.1-flash" {
		t.Errorf("回调收到的模型名=%q，必须原样透传（realm 前缀剥离由注入侧负责）", gotModel)
	}
	if len(gotCandidates) != 3 {
		t.Errorf("回调收到的候选数=%d want 3（首绑时无绑定 → 全部可用号都是空闲号）: %v",
			len(gotCandidates), gotCandidates)
	}
}

// TestResolveForModelStickyHitSkipsPickForModel 命中已有绑定后不得再调回调：粘性的
// 意义就是「同一会话固定走同一账号」，每次命中都重新加权等于把粘性关掉。
func TestResolveForModelStickyHitSkipsPickForModel(t *testing.T) {
	r := routerWith(newCountingStore(), []string{"a1", "a2"}, time.Minute)
	calls := 0
	r.cfg.PickForModel = func(string, []string) string {
		calls++
		return "a2"
	}

	first, ok1 := r.ResolveForModel("c1", "m1")
	second, ok2 := r.ResolveForModel("c1", "m1")
	if !ok1 || !ok2 || first != "a2" || second != "a2" {
		t.Fatalf("两次解析应稳定命中 a2，got %q/%q ok=%v/%v", first, second, ok1, ok2)
	}
	if calls != 1 {
		t.Errorf("回调被调用 %d 次，第二次应走粘性命中（want 1）", calls)
	}
}

// TestResolveForModelFallsBackToHashWhenPickReturnsEmpty 回调给出空串（候选被实时状态
// 全部滤掉）时必须回落哈希分配，而不是返回 ok=false 让整条请求失败。
func TestResolveForModelFallsBackToHashWhenPickReturnsEmpty(t *testing.T) {
	uids := []string{"a1", "a2", "a3"}
	r := routerWith(newCountingStore(), uids, time.Minute)
	r.cfg.PickForModel = func(string, []string) string { return "" }

	uid, ok := r.ResolveForModel("c1", "m1")
	if !ok {
		t.Fatal("回调返回空串时应回落哈希分配，不该 ok=false")
	}
	if want := uids[hashIndex("c1", len(uids))]; uid != want {
		t.Errorf("回落结果=%q want %q（hashIndex 旧语义）", uid, want)
	}
}

// TestResolveForModelRejectsOutOfCandidatePick 回调返回候选集外的 uid 时不得采信，
// 否则会把一个本不该参与本次分配的号绑给会话（粘性钉在不可用号上的老问题）。
func TestResolveForModelRejectsOutOfCandidatePick(t *testing.T) {
	uids := []string{"a1", "a2"}
	r := routerWith(newCountingStore(), uids, time.Minute)
	r.cfg.PickForModel = func(string, []string) string { return "not-in-candidates" }

	uid, ok := r.ResolveForModel("c1", "m1")
	if !ok {
		t.Fatal("越界 uid 应回落哈希分配，不该整条失败")
	}
	if uid != "a1" && uid != "a2" {
		t.Fatalf("绑定了候选集外的账号 %q", uid)
	}
}

// TestResolveForModelWithoutHookKeepsHashAllocation 未注入回调时行为必须与打补丁前
// 逐字一致（哈希取模），避免影响无 pool 依赖的调用方与既有部署。
//
// 每个 key 必须用**独立的 Router**：双段策略下，前一个会话绑定后会把它从"空闲号"
// 候选里摘掉，后续 hashIndex 的模数随之变化——共用一个 Router 就测不出哈希语义了。
func TestResolveForModelWithoutHookKeepsHashAllocation(t *testing.T) {
	uids := []string{"a1", "a2", "a3", "a4"}

	for _, key := range []string{"c1", "c2", "c3", "c4", "c5"} {
		r := routerWith(newCountingStore(), uids, time.Minute)
		uid, ok := r.ResolveForModel(key, "m1")
		if !ok {
			t.Fatalf("key=%s 解析失败", key)
		}
		if want := uids[hashIndex(key, len(uids))]; uid != want {
			t.Errorf("key=%s got %q want %q（未注入回调时应保持哈希语义）", key, uid, want)
		}
	}
}

// TestResolveForModelPickForModelOnModelSwitch 会话换模型触发重分配时，同样要按新模型
// 的候选集做加权——被 6004 限额后重绑的路径不能悄悄退回哈希。
func TestResolveForModelPickForModelOnModelSwitch(t *testing.T) {
	r := routerWith(newCountingStore(), []string{"a1", "a2"}, time.Minute)
	r.cfg.AvailableForModel = func(model string) []string {
		if model == "limited-model" {
			return []string{"a2"}
		}
		return []string{"a1", "a2"}
	}
	var models []string
	r.cfg.PickForModel = func(model string, candidates []string) string {
		models = append(models, model)
		return candidates[0] // 取该模型候选集里的第一个
	}

	if uid, ok := r.ResolveForModel("c1", "normal-model"); !ok || uid != "a1" {
		t.Fatalf("首次绑定应为 a1，got %q ok=%v", uid, ok)
	}
	// 换到限流模型：a1 不在候选里 → 命中失效 → 重分配，回调应被再次调用且拿到 a2。
	if uid, ok := r.ResolveForModel("c1", "limited-model"); !ok || uid != "a2" {
		t.Fatalf("换模型重分配应为 a2，got %q ok=%v", uid, ok)
	}
	if len(models) != 2 || models[1] != "limited-model" {
		t.Errorf("换模型时回调收到的模型序列=%v，want 第二次为 limited-model", models)
	}
}
