package pool

import (
	"testing"

	"workbuddy2api/internal/auth"
)

// issue #364：保底积分。低于阈值的账号只在没有别的候选时才被选中。
func TestCreditFloorSkipsLowBalance(t *testing.T) {
	p := New("")
	defer p.Close()
	for uid, credits := range map[string]int64{"rich": 500, "mid": 100, "low": 50} {
		p.Add(&auth.Auth{UID: uid})
		p.SetCredits(uid, credits)
	}
	p.SetCreditFloor(200)
	for i := 0; i < 20; i++ {
		got := p.Pick("")
		if got == nil {
			t.Fatal("不应为 nil")
		}
		if got.UID != "rich" {
			t.Fatalf("保底 200 时选中了 %s（credits=%d）", got.UID, p.creditOf(got.UID))
		}
	}
}

func (p *Pool) creditOf(uid string) int64 {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if e := p.byUID[uid]; e != nil {
		return e.credits
	}
	return -1
}

// 全部候选都低于保底时必须退化，而不是 503。
func TestCreditFloorFallsBackWhenAllLow(t *testing.T) {
	p := New("")
	defer p.Close()
	p.Add(&auth.Auth{UID: "a"})
	p.Add(&auth.Auth{UID: "b"})
	p.SetCredits("a", 10)
	p.SetCredits("b", 20)
	p.SetCreditFloor(1000)
	if got := p.Pick(""); got == nil {
		t.Fatal("全部低于保底时应回退到原候选，不能返回 nil")
	}
}

// credits==0 表示「还没同步到余额」，不能因为保底被排除。
func TestCreditFloorKeepsUnknownBalance(t *testing.T) {
	p := New("")
	defer p.Close()
	p.Add(&auth.Auth{UID: "unknown"})
	p.SetCreditFloor(100)
	if got := p.Pick(""); got == nil || got.UID != "unknown" {
		t.Fatalf("余额未知的账号不应被保底排除，got %+v", got)
	}
}

// 粘性号跌破保底时要让位：PickByUIDForModel 返回 nil，handler 会解绑换号。
func TestCreditFloorReleasesStickyBelowFloor(t *testing.T) {
	p := New("")
	defer p.Close()
	p.Add(&auth.Auth{UID: "sticky"})
	p.SetCredits("sticky", 50)
	p.SetCreditFloor(100)
	if got := p.PickByUIDForModel("sticky", ""); got != nil {
		t.Fatalf("低于保底的粘性号不应被命中: %+v", got)
	}
	p.SetCredits("sticky", 150)
	if got := p.PickByUIDForModel("sticky", ""); got == nil {
		t.Fatal("达到保底后粘性应恢复")
	}
}
