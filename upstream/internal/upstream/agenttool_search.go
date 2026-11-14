package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"workbuddy2api/internal/auth"
	"workbuddy2api/internal/session"
)

// ---------------------------------------------------------------------------
// agenttool 搜索（web search）：CLI 内置 WebSearchTool.performWebSearch 的同款上游接口
// ---------------------------------------------------------------------------

// agenttoolSearchTimeout 单次搜索的硬超时。对齐官方 CLI WebSearchTool 的
// AbortController 20s 约定（逆向 docs/04-upstream-protocols.md §2）。
const agenttoolSearchTimeout = 20 * time.Second

// AgentToolSearchPath CN agenttool 域的搜索端点路径。
const AgentToolSearchPath = "/agenttool/v1/search"

// AgentToolSearchRequest agenttool/v1/search 请求体（官方 CLI buildRequestBody 逐字段）。
// BlockedDomains 不在此层处理：官方语义是把 blocked 域拼进 query 文本（" -site:x"），
// 由调用方（handler）完成拼接后传 Query。
type AgentToolSearchRequest struct {
	Query          string   `json:"query"`
	Type           string   `json:"type"` // 固定 "text2text"
	MaxResults     int      `json:"max_results"`
	AllowedDomains []string `json:"allowed_domains,omitempty"`
	Freshness      string   `json:"freshness,omitempty"` // "d1" | "m1" | "y1"
}

// AgentToolSearchResult 单条搜索结果（官方响应 results[] 形态）。
type AgentToolSearchResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Snippet string `json:"snippet"`
}

// AgentToolSearchResponse agenttool/v1/search 响应信封（原样透传给客户端，
// 网关不做 filterUnsafeResults —— 客户端过滤与否由调用方决定）。
type AgentToolSearchResponse struct {
	Code           int                     `json:"code"`
	Msg            string                  `json:"msg"`
	Results        []AgentToolSearchResult `json:"results"`
	TotalResults   int                     `json:"total_results"`
	ResponseTimeMs int64                   `json:"response_time_ms"`
	Provider       string                  `json:"provider"`
}

// IsQuotaExhausted 报告搜索响应是否为 web-search 日配额耗尽（业务码 15001，
// classifyErrorDetail: quota_web_search）。该配额是**账号级**的：
// 处置 = 该号软冷却 + 立即换号（与 6004 模型级冷却同构，但无模型豁免语义——
// 搜索没有模型维度）。
func (r *AgentToolSearchResponse) IsQuotaExhausted() bool {
	return r.Code == 15001
}

// agentToolSearchBase agenttool 域按账号 realm 分域（与 chatBase/billingBase
// 同一分域约定）：CN = www.workbuddy.cn，global = www.workbuddy.ai——两个
// product.json 的 endpoint 字段即各自域（CN app: workbuddy.cn；intl app:
// workbuddy.ai），且 /agenttool/v1/search 在两域均已验证存在（未带凭证探针
// 双双回 401，路由存在 + 要求鉴权）。AgentToolBaseOverride 非空时优先
// （测试注入 / 部署侧端点覆盖，覆盖时 realm 分域失效——整池同域）。
func (c *Client) agentToolSearchBase(a *auth.Auth) string {
	if c.AgentToolBaseOverride != "" {
		return c.AgentToolBaseOverride
	}
	if a != nil && a.IsGlobal() {
		return AgentToolBaseGlobal
	}
	return AgentToolBaseCN
}

// AgentToolSearch 用账号 a 执行一次 web search。失败返回 *upstream.Error
// （Kind 已分类），成功返回解析后的响应信封（Code/Msg 原样保留——业务码
// 15001 等由调用方按 IsQuotaExhausted 处置）。
//
// 头体系与官方 CLI WebSearchTool.performWebSearch 对齐（04 文档 §2 头表）：
//   - CommonHeaders：UA 三段式 / Origin/Referer / X-Requested-With /
//     X-CodeBuddy-Request / Accept-Language / X-Machine-ID / X-Session-ID
//   - injectAttribution：X-Agent-Purpose / X-Product / X-IDE-*（用量归因）
//   - Authorization: Bearer accessToken（搜索是账号业务面，非匿名接口）
//   - X-User-Id / 企业与域头：与 ChatHeaders 同款（账号维度必发）
//   - X-Device-Token：与 billing 同策略（resolveDeviceToken 三级兜底）
//   - 会话头族：搜索是无会话单轮，X-Conversation-ID 用请求级随机
//     （session.NewMessageID），不引入任何粘性键
func (c *Client) AgentToolSearch(ctx context.Context, a *auth.Auth, req AgentToolSearchRequest) (*AgentToolSearchResponse, error) {
	if req.Type == "" {
		req.Type = "text2text"
	}
	if req.MaxResults <= 0 {
		req.MaxResults = 5
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, &Error{Kind: ErrBadParams, Status: http.StatusBadRequest, Msg: "marshal search body: " + err.Error()}
	}

	ctx, cancel := context.WithTimeout(ctx, agenttoolSearchTimeout)
	defer cancel()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.agentToolSearchBase(a)+AgentToolSearchPath, bytes.NewReader(body))
	if err != nil {
		return nil, &Error{Kind: ErrBadParams, Status: http.StatusBadRequest, Msg: "build search request: " + err.Error()}
	}

	// 官方指纹头组（与 chat/billing 同管线，见 headers.go 注释）。
	c.CommonHeaders(httpReq, a)
	c.injectAttribution(httpReq)
	if at := a.AccessTokenValue(); at != "" {
		httpReq.Header.Set("Authorization", "Bearer "+at)
	} else {
		httpReq.Header.Set("X-No-Authorization", "1")
	}
	if a.UID != "" {
		httpReq.Header.Set("X-User-Id", a.UID)
	}
	if a != nil && !a.IsGlobal() {
		if a.EnterpriseID != "" {
			httpReq.Header.Set("X-Enterprise-Id", a.EnterpriseID)
		} else {
			httpReq.Header.Set("X-No-Enterprise-Id", "1")
		}
		if d := a.DomainValue(); d != "" {
			httpReq.Header.Set("X-Domain", d)
		} else {
			httpReq.Header.Set("X-No-Department-Info", "1")
		}
	}
	c.injectDeviceToken(httpReq, a)
	// 会话头族：请求级随机（无会话粘性——搜索无上下文，粘住单号会把该号的
	// 15001 日配额打穿）。复用 injectConversationHeaders 的完整 B3 链路。
	c.injectConversationHeaders(httpReq, ChatMeta{
		ConversationID:        session.NewMessageID(),
		ConversationRequestID: session.NewMessageID(),
	})

	client, cerr := c.clientFor(a, false) // 非 chat：走账号代理出口 + 120s client 兜底超时
	if cerr != nil {
		return nil, wrapTransport(a, cerr)
	}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, wrapTransport(a, err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, wrapTransport(a, fmt.Errorf("read search body: %w", err))
	}

	// 非成功状态：走统一分类（与 doJSON 同口径），调用方按 Kind 处置。
	if resp.StatusCode >= 400 {
		kind := Classify(resp.StatusCode, string(raw))
		if kind == ErrNone {
			kind = ErrClient
		}
		return nil, &Error{Kind: kind, Status: resp.StatusCode, Msg: truncate(string(raw), 200)}
	}

	var out AgentToolSearchResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, &Error{Kind: ErrServer, Status: resp.StatusCode, Msg: "search response parse failed: " + truncate(string(raw), 160)}
	}
	return &out, nil
}

// wrapTransport 把传输层错误包成 *Error（Kind=ErrServer 语义），日志带账号标签。
func wrapTransport(a *auth.Auth, err error) *Error {
	label := ""
	if a != nil {
		label = a.UID
	}
	_ = label // upstream client 已在更深处打日志；此处只包错误
	return &Error{Kind: ErrServer, Status: 0, Msg: truncate(err.Error(), 200)}
}

// agenttool 域常量（CLI product.json endpoint，两版 app 各自的域）。
const (
	AgentToolBaseCN     = "https://www.workbuddy.cn"
	AgentToolBaseGlobal = "https://www.workbuddy.ai"
)
