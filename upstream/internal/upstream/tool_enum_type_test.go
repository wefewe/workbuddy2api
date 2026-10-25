package upstream

import (
	"encoding/json"
	"testing"
)

// toolEnumBody 构造一个只带 tools 的请求体，便于断言改写结果。
func toolEnumBody(t *testing.T, params map[string]any) []byte {
	t.Helper()
	body := map[string]any{
		"model": "kimi-k3.1-pro",
		"messages": []any{
			map[string]any{"role": "user", "content": "hi"},
		},
		"tools": []any{
			map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":       "probe",
					"parameters": params,
				},
			},
		},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}

// prepareAndProperty 跑一遍出站改写，取回 tools[0].function.parameters.properties[key]。
func prepareAndProperty(t *testing.T, params map[string]any, key string) map[string]any {
	t.Helper()
	out := PrepareBodyOpt(toolEnumBody(t, params), false)
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal out: %v", err)
	}
	tools, _ := got["tools"].([]any)
	if len(tools) == 0 {
		t.Fatalf("tools 丢失: %s", out)
	}
	fn, _ := tools[0].(map[string]any)["function"].(map[string]any)
	props, _ := fn["parameters"].(map[string]any)["properties"].(map[string]any)
	prop, _ := props[key].(map[string]any)
	if prop == nil {
		t.Fatalf("属性 %q 丢失", key)
	}
	return prop
}

// enum 缺 type：按取值类型补 type，且不能改动 enum 本身。
func TestToolEnumWithoutTypeGetsInferredType(t *testing.T) {
	cases := []struct {
		name string
		enum []any
		want string
	}{
		{"strings", []any{"quick", "deep"}, "string"},
		{"integers", []any{float64(1), float64(2)}, "integer"},
		{"integers and float", []any{float64(1), float64(2.5)}, "number"},
		{"floats", []any{float64(0.5)}, "number"},
		{"booleans", []any{true, false}, "boolean"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			prop := prepareAndProperty(t, map[string]any{
				"type":       "object",
				"properties": map[string]any{"x": map[string]any{"enum": c.enum}},
			}, "x")
			if got := prop["type"]; got != c.want {
				t.Errorf("type=%v want %s", got, c.want)
			}
			enum, ok := prop["enum"].([]any)
			if !ok || len(enum) != len(c.enum) {
				t.Errorf("enum 被改动: %v", prop["enum"])
			}
		})
	}
}

// 已显式声明 type 的属性不得被覆盖。
func TestToolEnumExistingTypePreserved(t *testing.T) {
	prop := prepareAndProperty(t, map[string]any{
		"type":       "object",
		"properties": map[string]any{"x": map[string]any{"type": "string", "enum": []any{"a"}}},
	}, "x")
	if got := prop["type"]; got != "string" {
		t.Errorf("显式 type 被覆盖: %v", got)
	}
}

// 混合标量无法无损推断：不补 type（交给上游按原样报错，不臆造）。
func TestToolEnumMixedScalarsUntouched(t *testing.T) {
	prop := prepareAndProperty(t, map[string]any{
		"type":       "object",
		"properties": map[string]any{"x": map[string]any{"enum": []any{"a", float64(1)}}},
	}, "x")
	if _, has := prop["type"]; has {
		t.Errorf("混合标量不应补 type: %v", prop)
	}
}

// 嵌套位置（array.items / anyOf / $defs）同样要处理。
func TestToolEnumNestedPositions(t *testing.T) {
	params := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"arr": map[string]any{
				"type":  "array",
				"items": map[string]any{"enum": []any{"a", "b"}},
			},
			"choice": map[string]any{
				"anyOf": []any{
					map[string]any{"enum": []any{float64(1)}},
					map[string]any{"type": "string"},
				},
			},
		},
		"$defs": map[string]any{
			"mode": map[string]any{"enum": []any{"on", "off"}},
		},
	}
	out := PrepareBodyOpt(toolEnumBody(t, params), false)
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	tools, _ := got["tools"].([]any)
	fn, _ := tools[0].(map[string]any)["function"].(map[string]any)
	root, _ := fn["parameters"].(map[string]any)
	props, _ := root["properties"].(map[string]any)

	arr, _ := props["arr"].(map[string]any)
	items, _ := arr["items"].(map[string]any)
	if items["type"] != "string" {
		t.Errorf("array.items 未补 type: %v", items)
	}

	choice, _ := props["choice"].(map[string]any)
	alts, _ := choice["anyOf"].([]any)
	branch, _ := alts[0].(map[string]any)
	if branch["type"] != "integer" {
		t.Errorf("anyOf 分支未补 type: %v", branch)
	}

	defs, _ := root["$defs"].(map[string]any)
	mode, _ := defs["mode"].(map[string]any)
	if mode["type"] != "string" {
		t.Errorf("$defs 未补 type: %v", mode)
	}
}

// 非 tools 位置的 schema（如 response_format）不得被改写。
func TestNormalizeToolEnumTypesOnlyTouchesTools(t *testing.T) {
	body := map[string]any{
		"model":    "kimi-k3.1-pro",
		"messages": []any{map[string]any{"role": "user", "content": "hi"}},
		"response_format": map[string]any{
			"type":        "json_schema",
			"json_schema": map[string]any{"schema": map[string]any{"properties": map[string]any{"x": map[string]any{"enum": []any{"a"}}}}},
		},
	}
	raw, _ := json.Marshal(body)
	out := PrepareBodyOpt(raw, false)
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	rf, _ := got["response_format"].(map[string]any)
	schema, _ := rf["json_schema"].(map[string]any)
	sch, _ := schema["schema"].(map[string]any)
	props, _ := sch["properties"].(map[string]any)
	x, _ := props["x"].(map[string]any)
	if _, has := x["type"]; has {
		t.Errorf("response_format 不应该被改写: %v", x)
	}
}

// 旧式 functions[] 形态同样覆盖。
func TestNormalizeToolEnumTypesLegacyFunctions(t *testing.T) {
	body := map[string]any{
		"model":    "kimi-k3.1-pro",
		"messages": []any{map[string]any{"role": "user", "content": "hi"}},
		"functions": []any{
			map[string]any{
				"name":       "legacy",
				"parameters": map[string]any{"type": "object", "properties": map[string]any{"x": map[string]any{"enum": []any{"a"}}}},
			},
		},
	}
	raw, _ := json.Marshal(body)
	out := PrepareBodyOpt(raw, false)
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	fns, _ := got["functions"].([]any)
	fn, _ := fns[0].(map[string]any)
	params, _ := fn["parameters"].(map[string]any)
	props, _ := params["properties"].(map[string]any)
	x, _ := props["x"].(map[string]any)
	if x["type"] != "string" {
		t.Errorf("functions[] 未补 type: %v", x)
	}
}
