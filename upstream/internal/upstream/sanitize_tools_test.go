package upstream

import (
	"encoding/json"
	"strings"
	"testing"
)

// issue #395：tools[].function.description 与 system prompt 同源，同样要净化。
func TestSanitizeToolsRewritesDescription(t *testing.T) {
	tools := []any{
		map[string]any{"type": "function", "function": map[string]any{
			"name":        "read_file",
			"description": ccIdentity + " " + ccBranch,
			"parameters":  map[string]any{"type": "object"},
		}},
	}
	if !sanitizeTools(tools) {
		t.Fatal("sanitizeTools 没有报告改动")
	}
	fn := tools[0].(map[string]any)["function"].(map[string]any)
	desc, _ := fn["description"].(string)
	if strings.Contains(desc, ccIdentity) {
		t.Errorf("description 仍含指纹：%q", desc)
	}
	if fn["name"] != "read_file" {
		t.Errorf("工具名不该被改写：%v", fn["name"])
	}
}

func TestPrepareBodySanitizesTools(t *testing.T) {
	body := []byte(`{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}],` +
		`"tools":[{"type":"function","function":{"name":"read_file","description":"` + ccIdentity + `","parameters":{"type":"object"}}}]}`)
	out := PrepareBodyOptWithEffortsAndDefault(body, true, nil, nil)
	var obj map[string]any
	if err := json.Unmarshal(out, &obj); err != nil {
		t.Fatalf("not json: %v", err)
	}
	tools, _ := obj["tools"].([]any)
	desc := tools[0].(map[string]any)["function"].(map[string]any)["description"].(string)
	if strings.Contains(desc, ccIdentity) {
		t.Errorf("出站 tools.description 仍含指纹：%q", desc)
	}
}

func TestPrepareBodyLeavesToolsWhenSanitizeOff(t *testing.T) {
	body := []byte(`{"model":"glm-5.2","messages":[{"role":"user","content":"hi"}],` +
		`"tools":[{"type":"function","function":{"name":"read_file","description":"` + ccIdentity + `"}}]}`)
	out := PrepareBodyOptWithEffortsAndDefault(body, false, nil, nil)
	if !strings.Contains(string(out), ccIdentity) {
		t.Error("sanitize=false 时不应改写 tools.description")
	}
}
