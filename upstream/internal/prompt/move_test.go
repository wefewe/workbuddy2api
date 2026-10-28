package prompt

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMoveSystemToUserMovesTextAndKeepsUserContent(t *testing.T) {
	body := []byte(`{"model":"m","messages":[` +
		`{"role":"system","content":"PERSONA-A"},` +
		`{"role":"developer","content":"PERSONA-B"},` +
		`{"role":"user","content":"hello"},` +
		`{"role":"assistant","content":"hi"}]}`)
	out, ok := MoveSystemToUser(body)
	if !ok {
		t.Fatal("有 system 文本与 user 消息时应成功")
	}
	var req struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &req); err != nil {
		t.Fatalf("输出不是合法 JSON: %v", err)
	}
	if len(req.Messages) != 3 {
		t.Fatalf("两条 system 应合并为一条中性 system，got %d 条", len(req.Messages))
	}
	if req.Messages[0].Role != "system" || req.Messages[0].Content != Degraded {
		t.Fatalf("首条应换成中性提示词，got %+v", req.Messages[0])
	}
	if req.Messages[1].Role != "user" {
		t.Fatalf("第二条应仍是 user，got %+v", req.Messages[1])
	}
	if !strings.Contains(req.Messages[1].Content, "PERSONA-A") ||
		!strings.Contains(req.Messages[1].Content, "PERSONA-B") ||
		!strings.HasSuffix(req.Messages[1].Content, "hello") {
		t.Fatalf("system 文本应原样搬到 user 前部、原 user 内容在尾部，got %q", req.Messages[1].Content)
	}
	if req.Messages[2].Content != "hi" {
		t.Fatalf("后续消息应逐字不动，got %+v", req.Messages[2])
	}
}

func TestMoveSystemToUserBails(t *testing.T) {
	cases := map[string]string{
		"非 JSON":     `not json`,
		"无 messages": `{}`,
		"没有 system":  `{"messages":[{"role":"user","content":"hi"}]}`,
		"没有 user":    `{"messages":[{"role":"system","content":"s"}]}`,
		"user 非纯文本": `{"messages":[{"role":"system","content":"s"},` +
			`{"role":"user","content":[{"type":"text","text":"hi"}]}]}`,
	}
	for name, body := range cases {
		if _, ok := MoveSystemToUser([]byte(body)); ok {
			t.Errorf("%s 应保守返回 false", name)
		}
	}
}
