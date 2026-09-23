package entity

import "xui/database/model"

// NodeRow 是被控端节点在列表页的展示形态：嵌入 ManagedNode 本体，
// 并附加该节点在管理端本地的入站/流量聚合统计。
type NodeRow struct {
	model.ManagedNode
	InboundCount int    `json:"inboundCount"`
	Used         int64  `json:"used"`
	UsedText     string `json:"usedText"`
	EnabledCount int    `json:"enabledCount"`
}

type ServerSetting struct {
	AutoDisable               bool   `json:"autoDisable" form:"autoDisable"`
	HeartbeatMinutes          int    `json:"heartbeatMinutes" form:"heartbeatMinutes"`
}

type ServerTraffic struct {
	Port   int   `json:"port"`
	Up     int64 `json:"up"`
	Down   int64 `json:"down"`
	Used   int64 `json:"used"`
	Total  int64 `json:"total"`
	Enable bool  `json:"enable"`
}
type TrafficSummary struct {
	// InboundId 是入站主键，页面按它拉取该账号的月度留档。
	InboundId int    `json:"inboundId"`
	Username  string `json:"username"`
	Port      int    `json:"port"`
	Local     int64  `json:"local"`
	Remote    int64  `json:"remote"`
	Total     int64  `json:"total"`
	Limit     int64  `json:"limit"`
	Enable    bool   `json:"enable"`
	// MonthlyReset 对应入站的“按月计算”开关：勾选的账号按月清零、按月留档；
	// 未勾选的累计计费，流量用完即止。Limit 为 0 时两种模式都不限量。
	MonthlyReset bool `json:"monthlyReset"`
	// Overlimit 表示本月汇总用量（本地 + 远程）已达套餐上限。
	Overlimit bool `json:"overlimit"`
	// Status 是三态筛选的取值：enabled／disabled／overlimit，与页面筛选一一对应。
	Status string `json:"status"`
	// 以下为按 1024 进制换算后的展示文本（KB/MB/GB），供页面直接渲染。
	LocalText  string `json:"localText"`
	RemoteText string `json:"remoteText"`
	TotalText  string `json:"totalText"`
	LimitText  string `json:"limitText"`
}

// TrafficSnapshot 是月度流量留档的对外形态，一行代表某账号某个月。
// Yyyymm 是月份编号（202609），Period 是同一含义的展示形式（2026-09）。
type TrafficSnapshot struct {
	Yyyymm    int    `json:"yyyymm"`
	Period    string `json:"period"`
	InboundId int    `json:"inboundId"`
	Port      int    `json:"port"`
	Remark    string `json:"remark"`
	Local     int64  `json:"local"`
	Remote    int64  `json:"remote"`
	Used      int64  `json:"used"`
	Limit     int64  `json:"limit"`
	// 其余为按 1024 进制换算后的展示文本，与流量汇总保持同一口径。
	LocalText  string `json:"localText"`
	RemoteText string `json:"remoteText"`
	UsedText   string `json:"usedText"`
	LimitText  string `json:"limitText"`
	ResetAt    int64  `json:"resetAt"`
}
