package server

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"
	"time"

	"workbuddy2api/internal/logfmt"
	"workbuddy2api/internal/pool"
	"workbuddy2api/internal/upstream"
)

// ---------------------------------------------------------------------------
// /v1/tools/search —— agenttool web search 透传（账号池轮转 + 15001 配额冷却）
// ---------------------------------------------------------------------------

// toolSearchRequest /v1/tools/search 入站请求体。
type toolSearchRequest struct {
	Query          string   `json:"query"`
	MaxResults     int      `json:"max_results"`
	AllowedDomains []string `json:"allowed_domains"`
	BlockedDomains []string `json:"blocked_domains"`
	Freshness      string   `json:"freshness"`
}

// toolSearchValidity 搜索配额冷却的统一时长（账号级，无模型豁免语义）。
// 15001 是日配额：与硬冷却不同，它随当日重置自然恢复，软冷却到次日 04:00 的
// 既有 CooldownUntilTomorrow4AM 语义最贴近（签到/配额刷新窗口一致），
// 但保守起见先给短软冷却——若上游日初即重置，短冷却让号更早回池；
// 持续 15001 的号会被后续请求反复冷却，自然收敛到“全天不可用”。
const toolSearchQuotaCooldown = 10 * time.Minute

// toolSearch handler：POST /v1/tools/search
//
// 选号语义（与 chatCompletions 的关键差异）：
//   - **不走会话粘性**：搜索无上下文，粘住单号会把该号的 15001 搜索日配额打穿。
//     每次请求纯轮换（PickExcludingForRealm，reqModel="" → 无模型豁免维度）。
//   - realm 支持：请求体 model 字段可带 "[cn:]" / "[global:]" 前缀（与 chat 同协议，
//     可选；不传则全池）。
//   - 15001（quota_web_search）→ 账号软冷却 + 换号重试；全部号配额耗尽 → 429 透传。
//   - global realm：上游无公开 agenttool 域，选到 global 号即跳过（不试错）。
func (h *Handler) toolSearch(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	var req toolSearchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "invalid request body: " + err.Error(),
		})
		return
	}
	req.Query = strings.TrimSpace(req.Query)
	if req.Query == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "query is required",
		})
		return
	}
	if req.MaxResults <= 0 {
		req.MaxResults = 5
	}
	if req.MaxResults > 20 {
		req.MaxResults = 20
	}
	switch req.Freshness {
	case "", "d1", "m1", "y1":
	default:
		writeJSON(w, http.StatusBadRequest, map[string]any{
			"error": "freshness must be one of: d1, m1, y1",
		})
		return
	}

	// blocked_domains：官方 CLI 语义是拼进 query 文本（" -site:domain"）。
	// 在网关层完成拼接，调用方（DSH 插件）无需自行处理。
	q := req.Query
	for _, d := range req.BlockedDomains {
		d = strings.TrimSpace(d)
		if d != "" {
			q += " -site:" + d
		}
	}

	upReq := upstream.AgentToolSearchRequest{
		Query:          q,
		Type:           "text2text",
		MaxResults:     req.MaxResults,
		AllowedDomains: req.AllowedDomains,
		Freshness:      req.Freshness,
	}

	tried := map[string]bool{}
	var lastBody string
	var lastStatus int
	quotaExhausted := 0 // 15001 计数：全部号配额耗尽时末端语义化

	for i := 0; i < h.cfg.MaxRotate; i++ {
		acct := h.cfg.Pool.PickExcludingForRealm(tried, "", "") // 无模型、无粘性、纯轮换
		if acct == nil {
			break
		}
		// realm 由账号本身决定（agentToolSearchBase 分域），CN/global 都可搜。
		tried[acct.UID] = true
		if !h.cfg.Pool.Acquire(acct.UID) {
			continue // 在途满：换号
		}

		resp, err := h.cfg.Upstream.AgentToolSearch(r.Context(), acct, upReq)
		h.cfg.Pool.Release(acct.UID)

		var uerr *upstream.Error
		if errors.As(err, &uerr) {
			lastStatus = uerr.Status
			lastBody = uerr.Msg
			switch uerr.Kind {
			case upstream.ErrSoftRate, upstream.ErrAccountFault, upstream.ErrWafBlock,
				upstream.ErrServer, upstream.ErrNotFound:
				// 账号级可轮转故障：走既有错误策略（冷却/计数）+ 换号。
				h.applyErrorPolicy(acct.UID, uerr.Kind, uerr.Msg, "", uerr)
				h.cfg.Pool.NoteError(acct.UID)
				continue
			case upstream.ErrSessionDead, upstream.ErrAuthRejected:
				h.applyErrorPolicy(acct.UID, uerr.Kind, uerr.Msg, "", uerr)
				continue
			default:
				// ErrClient / ErrBadParams / 其他：请求问题，不罚号不轮转，透传。
				writeJSON(w, http.StatusBadRequest, map[string]any{
					"error": "search request rejected by upstream",
					"code":  int(uerr.Kind),
					"msg":   uerr.Msg,
				})
				log.Printf("WARN: [server] search client error acct=%s: %s", logfmt.Label(acct.UID, acct.Nickname), uerr.Msg)
				return
			}
		}
		if err != nil {
			// 非分类错误（marshal 等本地问题）：透传 500。
			writeJSON(w, http.StatusInternalServerError, map[string]any{"error": err.Error()})
			return
		}

		// 上游 200 但业务码非 0（搜索域的业务信封，如 15001）。
		if resp.Code != 0 {
			lastBody = resp.Msg
			lastStatus = http.StatusTooManyRequests
			if resp.IsQuotaExhausted() {
				quotaExhausted++
				// 15001 是账号级搜索日配额：软冷却该号（不熔断不禁用），换号重试。
				// 固定时长软冷却（不走 CooldownSoftRate 的 streak 退避：搜索配额
				// 与请求频率无关，固定 10 分钟即语义；恢复由上游日重置或冷却到期）。
				h.cfg.Pool.Cooldown(acct.UID, pool.CoolSoft, toolSearchQuotaCooldown, "15001 web search quota")
				log.Printf("INFO: [server] search quota exhausted acct=%s, rotating", logfmt.Label(acct.UID, acct.Nickname))
				continue
			}
			// 其他业务码：原样透传（与 chat 的 error-passthrough 同哲学）。
			writeJSON(w, http.StatusOK, resp)
			return
		}

		// 成功：原样透传上游信封（results/total_results/response_time_ms/provider）。
		h.cfg.Pool.NoteSuccess(acct.UID)
		log.Printf("INFO: [server] search ok acct=%s results=%d ms=%d total=%s",
			logfmt.Label(acct.UID, acct.Nickname), len(resp.Results), resp.ResponseTimeMs,
			time.Since(start).Round(time.Millisecond))
		writeJSON(w, http.StatusOK, resp)
		return
	}

	// 轮转结束仍无成功。
	if quotaExhausted > 0 {
		// 全池搜索配额耗尽：429 + 语义化提示（DSH 插件按此提示用户）。
		writeJSON(w, http.StatusTooManyRequests, map[string]any{
			"error":  "web search quota exhausted on all available accounts (upstream code 15001); quota resets daily",
			"code":   "quota_web_search",
			"tried":  quotaExhausted,
			"detail": lastBody,
		})
		return
	}
	status := http.StatusServiceUnavailable
	if lastStatus >= 400 {
		status = lastStatus
	}
	writeJSON(w, status, map[string]any{
		"error":  "no account available for web search",
		"detail": lastBody,
	})
}
