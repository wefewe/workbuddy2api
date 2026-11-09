package main

import (
	"os"
	"path/filepath"
	"testing"

	"workbuddy2api/internal/upstream"
)

// issue #396：改 config.json 后词表可热重载；解析失败保留旧词表。
func TestReloadSanitizeConfig(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	write := func(s string) {
		if err := os.WriteFile(path, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(`{"features":{"sanitize_extra_features":["foo-phrase"],"sanitize_extra_rewrites":[["a","b"]]}}`)
	if !reloadSanitizeConfig(path) {
		t.Fatal("首次热重载失败")
	}
	f, r := upstream.SanitizeExtraCounts()
	if f != 1 || r != 1 {
		t.Fatalf("词表数量不对：features=%d rewrites=%d", f, r)
	}

	write(`{"features":{"sanitize_extra_features":["x","y"]}}`)
	if !reloadSanitizeConfig(path) {
		t.Fatal("第二次热重载失败")
	}
	f, r = upstream.SanitizeExtraCounts()
	if f != 2 || r != 0 {
		t.Fatalf("整体替换语义不对：features=%d rewrites=%d", f, r)
	}

	write(`{not-json`)
	if reloadSanitizeConfig(path) {
		t.Fatal("坏配置不该报告成功")
	}
	if f, _ := upstream.SanitizeExtraCounts(); f != 2 {
		t.Fatalf("坏配置把旧词表冲掉了：%d", f)
	}
}
