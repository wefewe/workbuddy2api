package pool

import (
	"encoding/json"
	"testing"
	"time"
	"workbuddy2api/internal/auth"
)

func TestRefreshRiskBoundsExpiryAndIndependence(t *testing.T) {
	for _, tc := range []struct{ input, want time.Duration }{{-time.Hour, 5 * time.Minute}, {0, 5 * time.Minute}, {time.Second, 5 * time.Minute}, {15 * time.Minute, 15 * time.Minute}, {24 * time.Hour, 6 * time.Hour}} {
		p := New("")
		p.Add(&auth.Auth{UID: "risk"})
		p.Add(&auth.Auth{UID: "healthy"})
		now := time.Now()
		e := p.byUID["risk"]
		p.noteRefreshRateLimitedLocked(e, tc.input, now)
		if e.refreshRisk.Until != now.Add(tc.want).Unix() {
			t.Fatalf("wait=%v state=%+v", tc.input, e.refreshRisk)
		}
		if !e.refreshRiskActive(now) || e.healthy(now) || e.manualDisabled || e.disabled {
			t.Fatal("automatic hold changed independent flags")
		}
		if got := p.Pick(""); got == nil || got.UID != "healthy" {
			t.Fatalf("picked paused account: %+v", got)
		}
		if e.refreshRiskActive(now.Add(tc.want)) || !e.healthy(now.Add(tc.want)) {
			t.Fatal("hold did not expire")
		}
		e.manualDisabled = true
		e.disabled = true
		p.ClearRefreshRisk("risk")
		if !e.manualDisabled || !e.disabled {
			t.Fatal("clear erased operator/system state")
		}
	}
}

func TestRefreshRiskEscalationRequiresDistinctRecoveryCycles(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u"})
	e := p.byUID["u"]
	now := time.Now()
	p.noteRefreshRateLimitedLocked(e, 0, now)
	p.noteRefreshRateLimitedLocked(e, 0, now.Add(time.Second))
	if e.refreshRisk.Failures != 1 {
		t.Fatal("concurrent failure counted as recovery")
	}
	for i := 2; i <= 3; i++ {
		now = time.Unix(e.refreshRisk.Until, 0).Add(time.Second)
		p.noteRefreshRateLimitedLocked(e, 0, now)
		if e.refreshRisk.Failures != i {
			t.Fatal(e.refreshRisk)
		}
	}
	if !e.refreshRiskActive(now.Add(24*time.Hour)) || !e.refreshRisk.NeedsReview {
		t.Fatal("repeat failures not escalated")
	}
	p.NoteRefreshSuccess("u")
	if !e.refreshRisk.NeedsReview {
		t.Fatal("inflight success cleared review")
	}
	p.ClearRefreshRisk("u")
	if e.refreshRiskActive(time.Now()) {
		t.Fatal("explicit clear failed")
	}
}

func TestRefreshRiskLateFailureResetsStreakAndSuccessResets(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u"})
	e := p.byUID["u"]
	now := time.Now().Add(-time.Hour)
	p.noteRefreshRateLimitedLocked(e, 0, now)
	p.noteRefreshRateLimitedLocked(e, 0, time.Unix(e.refreshRisk.Until, 0).Add(6*time.Minute))
	if e.refreshRisk.Failures != 1 {
		t.Fatal("late failure escalated")
	}
	p.NoteRefreshSuccess("u")
	if e.refreshRisk.Failures != 0 || e.refreshRisk.LastFailureAt == 0 {
		t.Fatal("success must reset streak but retain warning")
	}
}

func TestRefreshRiskStructuredStateRoundTripAndReasonNotAuthority(t *testing.T) {
	p := New("")
	p.Add(&auth.Auth{UID: "u"})
	e := p.byUID["u"]
	e.manualReason = "account-risk: user-supplied reason"
	if e.refreshRiskActive(time.Now()) {
		t.Fatal("parsed manual reason")
	}
	p.PauseRefreshRateLimited("u", time.Hour)
	// Persisted state and /status have distinct state/derived status types.
	raw, err := json.Marshal(stateAccount{RefreshRisk: cloneRefreshRisk(e.refreshRisk)})
	if err != nil {
		t.Fatal(err)
	}
	var saved stateAccount
	if err = json.Unmarshal(raw, &saved); err != nil {
		t.Fatal(err)
	}
	restored := &entry{refreshRisk: cloneRefreshRisk(saved.RefreshRisk)}
	if !restored.refreshRiskActive(time.Now()) || restored.refreshRiskActive(time.Now().Add(2*time.Hour)) {
		t.Fatal("persistent deadline invalid")
	}
	st, _ := p.Status("u")
	if st.RefreshRisk == nil || !st.RefreshRisk.Paused || st.ManualDisabled || st.Disabled || st.CoolKind != "refresh_risk" {
		t.Fatalf("wrong status: %+v", st)
	}
}
