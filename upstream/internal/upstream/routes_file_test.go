package upstream

import (
	"os"
	"path/filepath"
	"testing"
)

// 独立二进制加载线路表的回归测试（issue #210 补充）。
//
// 这批断言锁的是「配了线路的工具必须真的拿到线路表」——此前 signin/trial/
// activity/credit 四个 cmd 各自 upstream.New() 后就发请求，线路表永远空，
// 绑了线路的账号一律报「出口线路不可用」，没绑的静默走家宽。

func writeJSON(t *testing.T, name, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatalf("write %s: %v", p, err)
	}
	return p
}

func TestLoadRoutesFromFileLoadsProxies(t *testing.T) {
	p := writeJSON(t, "config.json", `{
		"listen": ":7863",
		"proxies": {
			"yunserver": "socks5://u:p@127.0.0.1:17891",
			"lisa": "socks5://u:p@192.0.2.10:20291"
		}
	}`)
	t.Cleanup(func() { ConfigureProxies(nil) })

	names, err := LoadRoutesFromFile(p)
	if err != nil {
		t.Fatalf("LoadRoutesFromFile: %v", err)
	}
	if len(names) != 2 || names[0] != "lisa" || names[1] != "yunserver" {
		t.Fatalf("线路名 = %v, 期望 [lisa yunserver]", names)
	}
	// 线路必须真的能被解析成 Transport（账号 clientFor 会走这条路径）。
	if _, err := TransportFor("yunserver"); err != nil {
		t.Fatalf("TransportFor(yunserver): %v", err)
	}
}

// 没有 proxies 段不算错误：纯直连部署必须零回归。
func TestLoadRoutesFromFileWithoutProxies(t *testing.T) {
	p := writeJSON(t, "config.json", `{"listen": ":7863"}`)
	t.Cleanup(func() { ConfigureProxies(nil) })

	names, err := LoadRoutesFromFile(p)
	if err != nil {
		t.Fatalf("无 proxies 段不该报错，得到 %v", err)
	}
	if len(names) != 0 {
		t.Fatalf("无 proxies 段应得空表，得到 %v", names)
	}
}

// 读不到文件要报错，让调用方决定降级；静默当成「没有线路」正是问题成因。
func TestLoadRoutesFromFileMissing(t *testing.T) {
	p := filepath.Join(t.TempDir(), "nope.json")
	if _, err := LoadRoutesFromFile(p); err == nil {
		t.Fatal("缺失配置文件应返回错误")
	}
}

func TestLoadRoutesFromFileBadJSON(t *testing.T) {
	p := writeJSON(t, "config.json", `{"proxies": `)
	if _, err := LoadRoutesFromFile(p); err == nil {
		t.Fatal("非法 JSON 应返回错误")
	}
}

// $WB2A_CONFIG 优先级最高（面板/容器可覆盖默认路径）。
func TestConfigPathFromEnvPrefersEnv(t *testing.T) {
	t.Setenv("WB2A_CONFIG", "/tmp/wb2a-explicit.json")
	if got := ConfigPathFromEnv(); got != "/tmp/wb2a-explicit.json" {
		t.Fatalf("ConfigPathFromEnv() = %q", got)
	}
}

// LoadRoutesAndLog 三种结局都要留痕（配了没生效 / 没配 / 读不到）。
func TestLoadRoutesAndLogSpeaks(t *testing.T) {
	p := writeJSON(t, "config.json", `{"proxies":{"route-a":"socks5://u:p@127.0.0.1:1080"}}`)
	t.Setenv("WB2A_CONFIG", p)
	t.Cleanup(func() { ConfigureProxies(nil) })

	var lines []string
	names := LoadRoutesAndLog(func(format string, args ...any) {
		lines = append(lines, format)
	})
	if len(names) != 1 {
		t.Fatalf("应加载 1 条线路，得到 %v", names)
	}
	if len(lines) == 0 {
		t.Fatal("应输出一行加载结果日志")
	}
}
