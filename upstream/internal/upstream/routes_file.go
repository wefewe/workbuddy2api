package upstream

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// 账号级出口线路的「非 server 入口」加载器（issue #210 补齐）。
//
// 背景：线路表原先只在 cmd/server 启动时由 config 包解析后 ConfigureProxies 注入。
// 独立二进制（cmd/signin、cmd/trial、cmd/activity、cmd/credit）直接用
// upstream.New() 发上游请求，线路表为空，于是**账号绑了线路时这些工具全部报
// 「账号 X 出口线路不可用：上游没有配置任何线路」**——看起来像隔离生效了，实际是
// 工具根本跑不起来；不报错的那几条路径则退回进程级默认出口（家宽 IP）。
//
// 这里提供只读 `proxies` 段的最小加载器，与 cmd/server 的 Config.Proxies 同一份真源
// （同一个 config.json 的同一个键），不引入第二套线路配置。

// defaultConfigPath 容器内默认配置位置（与 docker-compose 的 -config 入参一致）。
const defaultConfigPath = "/app/conf/config.json"

// ConfigPathFromEnv 返回独立二进制应读取的 config.json 路径（不保证文件存在）。
//
// 解析顺序：$WB2A_CONFIG → 当前目录 config.json → /app/conf/config.json。
func ConfigPathFromEnv() string {
	if v := strings.TrimSpace(os.Getenv("WB2A_CONFIG")); v != "" {
		return v
	}
	if _, err := os.Stat("config.json"); err == nil {
		if abs, aerr := filepath.Abs("config.json"); aerr == nil {
			return abs
		}
		return "config.json"
	}
	return defaultConfigPath
}

// LoadRoutesFromFile 读取 config.json 的 proxies 段并注入线路表，返回已加载线路名（升序）。
//
// 语义与 cmd/server 一致：未配置 proxies 段 → 空表、nil 错误（不报错，行为等同「全部走默认出口」）；
// 文件读不到 / 解析失败 → 返回错误，调用方应打日志后继续（工具本身仍可用，只是绑了线路的账号会显式报错）。
func LoadRoutesFromFile(path string) ([]string, error) {
	if strings.TrimSpace(path) == "" {
		path = ConfigPathFromEnv()
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取配置 %s: %w", path, err)
	}
	var doc struct {
		Proxies map[string]string `json:"proxies"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("解析配置 %s: %w", path, err)
	}
	ConfigureProxies(doc.Proxies)
	return RouteNames(), nil
}

// LoadRoutesFromEnv 是 LoadRoutesFromFile 的默认路径版本，供各 cmd main 一行接线。
func LoadRoutesFromEnv() ([]string, error) {
	return LoadRoutesFromFile("")
}

// LoadRoutesAndLog 是独立二进制的标准接线：加载线路表并把结果打到 log。
//
// 三种结局都要留痕——「配了没生效」和「没配」在排障时看起来一样，正是 issue #210
// 那一类「以为隔离了其实没有」的成因。
func LoadRoutesAndLog(logf func(string, ...any)) []string {
	names, err := LoadRoutesFromEnv()
	switch {
	case err != nil:
		logf("出口线路未加载：%v（绑了线路的账号会显式报错，未绑的走默认出口）", err)
	case len(names) == 0:
		logf("出口线路：config.json 未配置 proxies 段，全部账号走默认出口")
	default:
		logf("出口线路已加载：%d 条（%s）", len(names), strings.Join(names, ", "))
	}
	return names
}
