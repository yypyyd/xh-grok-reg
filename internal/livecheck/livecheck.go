// Package livecheck 为 Grok 注册模块提供手动测活：
// 用已保存的账号凭据探测其当前是否仍然有效，只区分三态——
//
//	alive   ：凭据明确有效；
//	dead    ：凭据明确失效（如 401 / invalid_grant / 回到登录页）；
//	unknown ：网络错误、Cloudflare、429、5xx、超时等无法确定的情况。
//
// 关键约束：unknown 绝不等同于 dead——任何一次不确定的失败都不能把账号判死，
// 更不会删除账号。测活只写状态，由用户主动点击触发，不做后台定时轮询。
package livecheck

const (
	StatusAlive   = "alive"
	StatusDead    = "dead"
	StatusUnknown = "unknown"
)

// Chunk 是批量测活的增量回调：每完成一批就回传 id->status，
// 供上层即时写库、刷新页面进度。
type Chunk func(map[uint]string)

func emit(onChunk Chunk, m map[uint]string) {
	if onChunk != nil && len(m) > 0 {
		onChunk(m)
	}
}
