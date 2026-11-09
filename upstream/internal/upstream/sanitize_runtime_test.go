package upstream

import (
	"strings"
	"testing"
)

// issue #396：运行期词表可整体替换；热重载后新词立即生效、旧词消失。
func TestConfigureSanitizeReplacesAndApplies(t *testing.T) {
	// 改写词同时要进 features，否则预检不命中、改写层不会执行。
	ConfigureSanitize([]string{"hot-word-aaa", "bad-token"}, [][2]string{{"bad-token", "replaced-token"}})
	t.Cleanup(func() { ConfigureSanitize(nil, nil) })

	// features 是「检测触发词」：命中才会进入改写管线；rewrites 才是替换表。
	if !hasFingerprint("prefix hot-word-aaa suffix") {
		t.Fatal("额外特征未参与预检")
	}
	if out := sanitizeText("use bad-token here"); strings.Contains(out, "bad-token") {
		t.Fatalf("额外改写未生效：%q", out)
	}

	ConfigureSanitize([]string{"hot-word-bbb"}, nil)
	if hasFingerprint("prefix hot-word-aaa suffix") {
		t.Fatal("整体替换后旧特征仍在预检表里")
	}
	if !hasFingerprint("prefix hot-word-bbb suffix") {
		t.Fatal("新特征未进入预检表")
	}
}
