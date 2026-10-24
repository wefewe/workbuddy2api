package pool

import (
	"time"
)

// WeightedPickAmong 在给定候选 uid 集合内按三因子权重选一个账号（供会话粘性首绑）。
//
// 为什么需要它（issue #265）：会话粘性未命中时，分配此前是
// `pool2[hashIndex(key, len(pool2))]` —— FNV-1a 取模落在「可用 UID 字典序」的某个
// 位置上，与积分余额、快过期积分、闲置时长全部无关。而 pool 为「积分过期」专门设计的
// 三因子权重（expiringWeight，见 weightOf）只在 Pick 这条路径生效。粘性默认开启，
// 带会话标识的请求（conversation 四键 / prompt_cache_key / 首条 user 消息兜底）在第一
// 轮就完成绑定并滚动续期，**永远走不到 Pick** —— 于是「快过期积分优先消耗」在默认配置
// 下基本失效：一个 creditsExpiring=0 的号可以独占整段会话流量，而两个 expiring 占比
// 24% 的号一次都轮不到。
//
// 与 pick 的差异（有意为之）：
//   - 不做成本分层硬过滤。成本分层回答「本次请求走谁更便宜」，是即时决策；粘性首绑回答
//     「这段会话长期挂谁」。按单价硬过滤会让全部会话收敛到最便宜的号，反而加剧单一账号
//     垄断 —— 正是本 issue 要消解的现象。
//   - 不做 Top-5 截断。首绑对每个会话只发生一次，没有惊群压力；截断必须先排序，而等
//     权重时的排序会系统性偏向候选集里靠前的 uid（候选来自 sort.Strings），把「加权」
//     悄悄退化成「字典序」。
//   - 不写 lastUsed / usedSeq。绑定不等于消耗：真正的在途租约与使用序号由随后的
//     PickByUIDForModel / Acquire 推进。若在这里写 lastUsed，一次绑定就把闲置补偿清零，
//     而粘性会持续命中该号，等于整个 TTL 都背着这个惩罚。
//
// 返回空串表示候选集里此刻没有可用账号，调用方自行兜底（粘性路由回落哈希分配）。
func (p *Pool) WeightedPickAmong(candidates []string, model string) string {
	if len(candidates) == 0 {
		return ""
	}
	p.mu.RLock()
	defer p.mu.RUnlock()

	now := time.Now()
	// 候选集已由调用方按 realm + 模型可用性筛过；这里按池内实时状态再过滤一次，缩掉
	// 「筛完到此刻之间刚进冷却 / 刚被手动停用」的时间窗 —— 否则会把一个已经不可用的号
	// 绑给会话，下一跳又得解绑重来。
	live := make([]*entry, 0, len(candidates))
	var maxCredits int64
	for _, uid := range candidates {
		e, found := p.byUID[uid]
		if !found || !e.healthyForModel(now, model) {
			continue
		}
		live = append(live, e)
		if e.credits > maxCredits {
			maxCredits = e.credits
		}
	}
	if len(live) == 0 {
		return ""
	}
	// 权重口径用**本候选集**最大 credits（而非全池）：粘性首绑只在给定候选集内做选择，
	// 用全池最大值会让本集合内的 credits 项整体缩水，快过期与闲置两项被动放大。
	ws := make([]weighted, len(live))
	for i, e := range live {
		ws[i] = weighted{e: e, w: p.weightOf(e, maxCredits, now)}
	}
	return p.pickWeighted(ws).a.UID
}
