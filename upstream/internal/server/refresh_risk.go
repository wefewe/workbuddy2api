package server

import (
	"encoding/json"
	"io"
	"net/http"
	"time"
)

func (h *Handler) adminRefreshRisk(w http.ResponseWriter, r *http.Request) {
	uid, ok := adminUID(w, r)
	if !ok {
		return
	}
	if _, found := h.cfg.Pool.Status(uid); !found {
		writeOpenAIError(w, 404, "not_found", "account not found")
		return
	}
	var body struct {
		RetryAfterSec int64 `json:"retry_after_sec"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&body); err != nil {
		writeOpenAIError(w, 400, "invalid_request", "expected retry_after_sec")
		return
	}
	// Clamp before conversion to avoid duration overflow from untrusted JSON.
	if body.RetryAfterSec < 300 {
		body.RetryAfterSec = 300
	}
	if body.RetryAfterSec > 21600 {
		body.RetryAfterSec = 21600
	}
	h.cfg.Pool.PauseRefreshRateLimited(uid, time.Duration(body.RetryAfterSec)*time.Second)
	status, _ := h.cfg.Pool.Status(uid)
	writeJSON(w, 200, map[string]any{"uid": uid, "refresh_risk": status.RefreshRisk})
}

func (h *Handler) adminClearRefreshRisk(w http.ResponseWriter, r *http.Request) {
	uid, ok := adminUID(w, r)
	if !ok {
		return
	}
	found, changed := h.cfg.Pool.ClearRefreshRisk(uid)
	if !found {
		writeOpenAIError(w, 404, "not_found", "account not found")
		return
	}
	status, _ := h.cfg.Pool.Status(uid)
	writeJSON(w, 200, map[string]any{"uid": uid, "changed": changed, "refresh_risk": status.RefreshRisk,
		"manual_disabled": status.ManualDisabled, "disabled": status.Disabled})
}

func (h *Handler) adminRefreshRiskSuccess(w http.ResponseWriter, r *http.Request) {
	uid, ok := adminUID(w, r)
	if !ok {
		return
	}
	if _, found := h.cfg.Pool.Status(uid); !found {
		writeOpenAIError(w, 404, "not_found", "account not found")
		return
	}
	h.cfg.Pool.NoteRefreshSuccess(uid)
	status, _ := h.cfg.Pool.Status(uid)
	writeJSON(w, 200, map[string]any{"uid": uid, "refresh_risk": status.RefreshRisk})
}
