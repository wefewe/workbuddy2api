package upstream

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
)

// 思考通道兜底（issue #135）：国际版 deepseek 思考模式下正文只在
// reasoning_content 里，只看 content 的客户端会显示空回复。
//
// 契约（刻意很窄）：
//
//	· 整轮**既无正文、也无工具调用** → 把思考原文同时作为 content 下发；
//	· 有正文 / 有工具调用的正常轮次 → 零改动；
//	· 开关关闭 → 与旧行为逐字节一致。
func withReasoningFallback(t *testing.T, on bool) {
	t.Helper()
	old := ReasoningFallbackEnabled()
	ConfigureReasoningFallback(on)
	t.Cleanup(func() { ConfigureReasoningFallback(old) })
}

func sseFrame(t *testing.T, delta map[string]any) string {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"id": "chatcmpl-x", "object": "chat.completion.chunk", "created": 1,
		"choices": []any{map[string]any{"index": 0, "delta": delta}},
	})
	if err != nil {
		t.Fatalf("marshal frame: %v", err)
	}
	return "data: " + string(raw)
}

func messageOf(t *testing.T, resp map[string]any) map[string]any {
	t.Helper()
	choices, _ := resp["choices"].([]any)
	if len(choices) == 0 {
		t.Fatalf("no choices in %v", resp)
	}
	c, _ := choices[0].(map[string]any)
	msg, _ := c["message"].(map[string]any)
	if msg == nil {
		t.Fatalf("no message in %v", c)
	}
	return msg
}

func TestAggregateBackfillsReasoningAsContent(t *testing.T) {
	withReasoningFallback(t, true)
	raw := strings.Join([]string{
		sseFrame(t, map[string]any{"role": "assistant", "reasoning_content": "思考A"}),
		"",
		sseFrame(t, map[string]any{"reasoning_content": "思考B"}),
		"",
		"data: [DONE]",
		"",
	}, "\n")

	resp, err := Aggregate(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	msg := messageOf(t, resp)
	if got := msg["content"]; got != "思考A思考B" {
		t.Errorf("content=%v want 思考A思考B（整轮无正文时应回填思考原文）", got)
	}
	// 思考原文仍保留：客户端要自己渲染时不能丢数据
	if got := msg["reasoning_content"]; got != "思考A思考B" {
		t.Errorf("reasoning_content=%v want 思考A思考B（回填不应删掉原字段）", got)
	}
}

func TestAggregateKeepsContentWhenPresent(t *testing.T) {
	withReasoningFallback(t, true)
	raw := strings.Join([]string{
		sseFrame(t, map[string]any{"reasoning_content": "思考"}),
		"",
		sseFrame(t, map[string]any{"content": "正文"}),
		"",
		"data: [DONE]",
		"",
	}, "\n")
	resp, err := Aggregate(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	if got := messageOf(t, resp)["content"]; got != "正文" {
		t.Errorf("content=%v want 正文（有正文时不得改写）", got)
	}
}

func TestAggregateSkipsBackfillWhenToolCalls(t *testing.T) {
	withReasoningFallback(t, true)
	raw := strings.Join([]string{
		sseFrame(t, map[string]any{"reasoning_content": "思考"}),
		"",
		sseFrame(t, map[string]any{"tool_calls": []any{map[string]any{
			"index": 0, "id": "call_1", "type": "function",
			"function": map[string]any{"name": "f", "arguments": "{}"},
		}}}),
		"",
		"data: [DONE]",
		"",
	}, "\n")
	resp, err := Aggregate(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	msg := messageOf(t, resp)
	if got := msg["content"]; got != "" {
		t.Errorf("content=%v want 空（有工具调用时不得把思考当正文，否则客户端会多显示一段"+
			"并可能误判为普通文本回答）", got)
	}
}

func TestAggregateFallbackCanBeDisabled(t *testing.T) {
	withReasoningFallback(t, false)
	raw := strings.Join([]string{
		sseFrame(t, map[string]any{"reasoning_content": "思考"}),
		"",
		"data: [DONE]",
		"",
	}, "\n")
	resp, err := Aggregate(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	msg := messageOf(t, resp)
	if got := msg["content"]; got != "" {
		t.Errorf("content=%v want 空（开关关闭时保持旧行为）", got)
	}
}

// 流式：收尾前一帧把思考补成 content；正常轮次不多发帧。
func TestStreamBackfillsReasoningAsContent(t *testing.T) {
	withReasoningFallback(t, true)
	raw := strings.Join([]string{
		sseFrame(t, map[string]any{"role": "assistant", "reasoning_content": "思考A"}),
		"",
		sseFrame(t, map[string]any{"reasoning_content": "思考B"}),
		"",
		"data: [DONE]",
		"",
	}, "\n")
	rec := httptest.NewRecorder()
	if err := StreamHint(rec, strings.NewReader(raw), nil); err != nil {
		t.Fatalf("stream: %v", err)
	}
	out := rec.Body.String()
	if !strings.Contains(out, `"content":"思考A思考B"`) {
		t.Errorf("流里没有补发的正文帧：\n%s", out)
	}
	// 补发帧必须在 [DONE] 之前
	if strings.Index(out, `"content":"思考A思考B"`) > strings.Index(out, "[DONE]") {
		t.Errorf("补发帧出现在 [DONE] 之后：\n%s", out)
	}
}

func TestStreamKeepsNormalTurnsUntouched(t *testing.T) {
	withReasoningFallback(t, true)
	raw := strings.Join([]string{
		sseFrame(t, map[string]any{"reasoning_content": "思考"}),
		"",
		sseFrame(t, map[string]any{"content": "正文"}),
		"",
		"data: [DONE]",
		"",
	}, "\n")
	rec := httptest.NewRecorder()
	if err := StreamHint(rec, strings.NewReader(raw), nil); err != nil {
		t.Fatalf("stream: %v", err)
	}
	out := rec.Body.String()
	// 有正文时不得再补一帧把思考当正文（否则客户端显示两遍）
	if strings.Contains(out, `"content":"思考"`) {
		t.Errorf("有正文的轮次被补发了思考帧：\n%s", out)
	}
}

func TestStreamBackfillCanBeDisabled(t *testing.T) {
	withReasoningFallback(t, false)
	raw := strings.Join([]string{
		sseFrame(t, map[string]any{"reasoning_content": "思考"}),
		"",
		"data: [DONE]",
		"",
	}, "\n")
	rec := httptest.NewRecorder()
	if err := StreamHint(rec, strings.NewReader(raw), nil); err != nil {
		t.Fatalf("stream: %v", err)
	}
	if strings.Contains(rec.Body.String(), `"content":"思考"`) {
		t.Errorf("开关关闭时仍然补发了正文：\n%s", rec.Body.String())
	}
}
