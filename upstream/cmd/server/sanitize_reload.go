package main

// 配置热重载：features.sanitize_extra_features / _rewrites（issue #396）。
//
// 上游黑名单是逐词扩充的，词表做成配置项后运维仍要重启容器才能生效。重启会
// 断开在途请求（挂了池的下游每次都要吃一次抖动），而词表本身是**整体替换**
// 语义（ConfigureSanitize 幂等），可以安全地重入。
//
// 这里只监听 config.json 的 mtime+size，变了就重新 Load 并把两张词表注入；
// 解析失败只记日志、保留旧词表，不能让一次手误把正在跑的净化关掉。

import (
	"log"
	"os"
	"time"

	"workbuddy2api/internal/upstream"
)

const sanitizeWatchInterval = 10 * time.Second

// reloadSanitizeConfig 读一次配置并注入词表；返回是否成功。
func reloadSanitizeConfig(path string) bool {
	cfg, err := Load(path)
	if err != nil {
		log.Printf("WARN: 净化词表热重载跳过（配置读取失败）：%v", err)
		return false
	}
	upstream.ConfigureSanitize(cfg.Features.SanitizeExtraFeatures,
		cfg.Features.SanitizeExtraRewrites)
	f, r := upstream.SanitizeExtraCounts()
	log.Printf("净化词表已热重载：features=%d rewrites=%d（config.json 变更）", f, r)
	return true
}

// watchSanitizeConfig 后台轮询配置文件；stop 关闭时退出。
func watchSanitizeConfig(path string, stop <-chan struct{}) {
	if path == "" {
		return
	}
	stamp := func() (int64, int64) {
		st, err := os.Stat(path)
		if err != nil {
			return 0, 0
		}
		return st.ModTime().UnixNano(), st.Size()
	}
	lastMod, lastSize := stamp()
	ticker := time.NewTicker(sanitizeWatchInterval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			mod, size := stamp()
			if mod == 0 {
				continue // 文件暂时不可读（编辑器原子替换的间隙）：下轮再看
			}
			if mod == lastMod && size == lastSize {
				continue
			}
			lastMod, lastSize = mod, size
			reloadSanitizeConfig(path)
		}
	}
}
