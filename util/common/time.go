package common

import "time"

// ShanghaiLocation 是中国上海时区（Asia/Shanghai）。
// 业务上的"时分秒"与"某年某月"判断统一使用该固定时区，
// 不依赖运行服务器的系统时区，避免不同机器上月度清零等定时逻辑错位。
var ShanghaiLocation = mustLoadLocation("Asia/Shanghai")

func mustLoadLocation(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		// Asia/Shanghai 是 POSIX 系统内置的常用时区；极端情况下加载失败，
		// 退化为 UTC+8 固定偏移，保证逻辑仍按东八区运行。
		return time.FixedZone("CST", 8*60*60)
	}
	return loc
}

// NowCN 返回按上海时区（东八区）解释的当前时间。
// 与 time.Now() 的绝对时刻一致，但取其年月/时分时按东八区换算。
func NowCN() time.Time {
	return time.Now().In(ShanghaiLocation)
}