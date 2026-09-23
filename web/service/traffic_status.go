package service

// 已知入站状态取值。被控端缺少新鲜数据时，汇总页另显示 unknown。
// 之所以收在一处并对外暴露，是因为“已超限”这个判断同时被四个地方依赖：
// 流量汇总接口、入站列表接口、超限自动停用任务、月初清零时的自动启用。
// 各处各算一套的话，迟早会出现“汇总页说超限、列表页说没超限”这种对不上的情况。
const (
	TrafficStatusEnabled   = "enabled"
	TrafficStatusDisabled  = "disabled"
	TrafficStatusOverlimit = "overlimit"
)

// TrafficOverlimit 判断本月用量是否已达套餐上限。
// 传入的是管理端记录的用量与所有被控端的汇总用量。
// limit <= 0 表示不限量，永远不会超限。
func TrafficOverlimit(local, remote, limit int64) bool {
	return limit > 0 && local+remote >= limit
}

// TrafficStatusOf 把“是否启用”与“是否超限”归到三态之一，三者互斥且完备：
// 仍处于启用状态的一律算启用（哪怕当下已超限，也要等停用后才归入超限）；
// 已停用的再看是否超限，超限归 overlimit，否则归 disabled。
func TrafficStatusOf(enable, overlimit bool) string {
	if enable {
		return TrafficStatusEnabled
	}
	if overlimit {
		return TrafficStatusOverlimit
	}
	return TrafficStatusDisabled
}
