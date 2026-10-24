package pool

import (
	"math/rand/v2"
	"testing"
	"time"

	"workbuddy2api/internal/auth"
)

// newDeterministicSource 固定种子的确定性随机源：加权分布断言在 CI 上必须可复现，
// 否则偶发越界会让本组用例变成 flaky。
func newDeterministicSource() func(n int64) int64 {
	src := rand.New(rand.NewPCG(20251023, 265))
	return func(n int64) int64 { return src.Int64N(n) }
}

// TestWeightedPickAmongPrefersExpiring 核心语义（issue #265）：候选集内快过期积分
// 占比高的账号，被粘性首绑选中的概率必须显著更高——这正是 Pick 的三因子权重里
// expiringWeight 的意图，也是本补丁要让粘性首绑继承的语义。
func TestWeightedPickAmongPrefersExpiring(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "plain"})
	p.Add(&auth.Auth{UID: "expiring"})
	p.SetCreditsDetailed("plain", 5000, 0)
	p.SetCreditsDetailed("expiring", 5000, 5000)
	p.SetRandomSource(newDeterministicSource())

	counts := map[string]int{}
	const rounds = 1000
	for i := 0; i < rounds; i++ {
		uid := p.WeightedPickAmong([]string{"plain", "expiring"}, "")
		if uid == "" {
			t.Fatalf("第 %d 次选号返回空串（候选两个账号均可用）", i)
		}
		counts[uid]++
	}
	// 权重：plain = 1 + 5000/5000×10 + 闲置满分 5 = 16；
	//       expiring = 1 + 10 + 1.0×8 + 5 = 24 → 期望 60% / 40%。
	if counts["expiring"] <= counts["plain"] {
		t.Fatalf("快过期号被选中 %d 次，未超过无快过期号的 %d 次（权重未生效）",
			counts["expiring"], counts["plain"])
	}
	if ratio := float64(counts["expiring"]) / rounds; ratio < 0.52 || ratio > 0.68 {
		t.Errorf("快过期号占比 %.3f 偏离期望 0.60（±0.08）", ratio)
	}
}

// TestWeightedPickAmongEmptyCandidates 空候选返回空串，不 panic、不随便选一个。
func TestWeightedPickAmongEmptyCandidates(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1"})
	if got := p.WeightedPickAmong(nil, ""); got != "" {
		t.Errorf("空候选应返回空串，got %q", got)
	}
	if got := p.WeightedPickAmong([]string{}, "m"); got != "" {
		t.Errorf("空候选应返回空串，got %q", got)
	}
}

// TestWeightedPickAmongSkipsUnavailable 手动停用 / 未知 uid 必须被排除：把不可用的号
// 绑给会话会让下一跳立刻解绑重分配，白跑一轮。
func TestWeightedPickAmongSkipsUnavailable(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "off"})
	p.SetManualDisabled("off", true, "维护中")

	if got := p.WeightedPickAmong([]string{"off", "ghost"}, ""); got != "" {
		t.Fatalf("候选全不可用时应返回空串，got %q", got)
	}
	if got := p.WeightedPickAmong([]string{"off", "ghost"}, ""); got == "off" || got == "ghost" {
		t.Fatalf("不可用账号被选中：%q", got)
	}
}

// TestWeightedPickAmongHonoursModelCooldown 被 6004 模型级冷却的账号在该模型上不可选，
// 但对其他模型仍可选（与 healthyForModel 的口径一致，勿在此处另立标准）。
func TestWeightedPickAmongHonoursModelCooldown(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "cooled"})
	p.Add(&auth.Auth{UID: "warm"})
	p.SetCredits("cooled", 1000)
	p.SetCredits("warm", 1000)
	// 用上游真实路径写冷却，不手工往 map 里塞（那样绕过 softRateMax 截断与 modelCooldowns 的初始化）。
	p.CooldownSoftForModel("cooled", time.Hour, time.Now().Add(time.Hour), "hy4-preview", "6004")

	if got := p.WeightedPickAmong([]string{"cooled", "warm"}, "hy4-preview"); got != "warm" {
		t.Errorf("模型级冷却号在该模型上不可选，got %q", got)
	}
	// 换一个未冷却的模型：两个都可用，冷却号必须重新进入候选（不因冷却而被永久摘除）。
	seen := map[string]bool{}
	p.SetRandomSource(newDeterministicSource())
	for i := 0; i < 200; i++ {
		seen[p.WeightedPickAmong([]string{"cooled", "warm"}, "other-model")] = true
	}
	if !seen["cooled"] {
		t.Error("其他模型上冷却号应重新可选，200 次抽样一次都没选中")
	}
}

// TestWeightedPickAmongKeepsSelectionState 绑定不等于消耗：本方法不得写 lastUsed /
// usedSeq，否则一次绑定就把闲置补偿清零，而粘性会持续命中该号，整个 TTL 都背着惩罚。
func TestWeightedPickAmongKeepsSelectionState(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u1"})
	p.SetCredits("u1", 100)
	p.SetRandomSource(newDeterministicSource())

	if got := p.WeightedPickAmong([]string{"u1"}, ""); got != "u1" {
		t.Fatalf("单候选应命中 u1，got %q", got)
	}
	p.mu.RLock()
	e := p.byUID["u1"]
	lastUsed, usedSeq, pickSeq := e.lastUsed, e.usedSeq, p.pickSeq
	p.mu.RUnlock()
	if !lastUsed.IsZero() {
		t.Errorf("lastUsed 被改写为 %s，粘性首绑不应推进使用时刻", lastUsed)
	}
	if usedSeq != 0 {
		t.Errorf("usedSeq 被改写为 %d，粘性首绑不应推进使用序号", usedSeq)
	}
	if pickSeq != 0 {
		t.Errorf("pickSeq 被改写为 %d，粘性首绑不应占用选号序号", pickSeq)
	}
}
