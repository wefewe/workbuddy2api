package pool

import "time"

const (
	RefreshRiskMinWait         = 5 * time.Minute
	RefreshRiskMaxWait         = 6 * time.Hour
	refreshRiskRepeatWindow    = 5 * time.Minute
	refreshRiskReviewThreshold = 3
)

// RefreshRiskState is independent of operator stops and system disablement.
// Epoch seconds survive restarts; Paused is derived, never persisted stale.
type RefreshRiskState struct {
	Until         int64 `json:"until"`
	LastFailureAt int64 `json:"last_failure_at"`
	Failures      int   `json:"failures"`
	NeedsReview   bool  `json:"needs_review"`
	RetryAfterSec int64 `json:"retry_after_sec"`
}

type RefreshRiskStatus struct {
	RefreshRiskState
	Paused bool `json:"paused"`
}

func cloneRefreshRisk(state *RefreshRiskState) *RefreshRiskState {
	if state == nil {
		return nil
	}
	copy := *state
	return &copy
}

func (e *entry) refreshRiskActive(now time.Time) bool {
	return e.refreshRisk != nil && (e.refreshRisk.NeedsReview || now.Unix() < e.refreshRisk.Until)
}

func (e *entry) refreshRiskStatus(now time.Time) *RefreshRiskStatus {
	if e.refreshRisk == nil {
		return nil
	}
	return &RefreshRiskStatus{RefreshRiskState: *e.refreshRisk, Paused: e.refreshRiskActive(now)}
}

func (p *Pool) RiskPaused(uid string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	e := p.byUID[uid]
	return e != nil && e.refreshRiskActive(time.Now())
}

func (p *Pool) PauseRefreshRateLimited(uid string, retryAfter time.Duration) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	e := p.byUID[uid]
	if e == nil {
		return false
	}
	p.noteRefreshRateLimitedLocked(e, retryAfter, time.Now())
	return true
}

func (p *Pool) noteRefreshRateLimitedLocked(e *entry, retryAfter time.Duration, now time.Time) {
	wait := retryAfter
	if wait < RefreshRiskMinWait {
		wait = RefreshRiskMinWait
	}
	if wait > RefreshRiskMaxWait {
		wait = RefreshRiskMaxWait
	}
	if e.refreshRisk == nil {
		e.refreshRisk = &RefreshRiskState{}
	}
	risk := e.refreshRisk
	if !e.refreshRiskActive(now) {
		if risk.Failures > 0 && risk.Until > 0 && now.Unix() <= risk.Until+int64(refreshRiskRepeatWindow.Seconds()) {
			risk.Failures++
		} else {
			risk.Failures = 1
		}
		if risk.Failures >= refreshRiskReviewThreshold {
			risk.NeedsReview = true
		}
	}
	until := now.Add(wait).Unix()
	if until > risk.Until {
		risk.Until = until
	}
	risk.LastFailureAt = now.Unix()
	risk.RetryAfterSec = int64(wait.Seconds())
	p.dirty.Store(true)
}

// A successful retry resets the streak. An in-flight response cannot clear an
// active pause or a manual-review escalation raised by another request.
func (p *Pool) NoteRefreshSuccess(uid string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e := p.byUID[uid]
	if e == nil || e.refreshRisk == nil || e.refreshRiskActive(time.Now()) {
		return
	}
	e.refreshRisk.Until = 0
	e.refreshRisk.Failures = 0
	p.dirty.Store(true)
}

// ClearRefreshRisk is an explicit admin action, orthogonal to enable/revive.
func (p *Pool) ClearRefreshRisk(uid string) (bool, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	e := p.byUID[uid]
	if e == nil {
		return false, false
	}
	if e.refreshRisk == nil {
		return true, false
	}
	changed := e.refreshRisk.Until != 0 || e.refreshRisk.NeedsReview || e.refreshRisk.Failures != 0
	e.refreshRisk.Until = 0
	e.refreshRisk.NeedsReview = false
	e.refreshRisk.Failures = 0
	p.dirty.Store(true)
	return true, changed
}
