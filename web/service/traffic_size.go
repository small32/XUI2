package service

import "strconv"

// 流量展示口径：按 1024 进制自动换算单位，保留两位小数。
// 不足 1 KB 时按字节显示整数；负数按 0 处理，避免出现负流量。
const (
	trafficKB = int64(1) << 10
	trafficMB = int64(1) << 20
	trafficGB = int64(1) << 30
)

// FormatTrafficSize 把字节数格式化为带单位的可读文本，例如 1024 -> "1.00 KB"。
// 换算门槛为大于等于 1024 逐级进位：KB、MB、GB。
func FormatTrafficSize(size int64) string {
	if size < 0 {
		size = 0
	}
	switch {
	case size >= trafficGB:
		return formatTrafficUnit(float64(size)/float64(trafficGB), "GB")
	case size >= trafficMB:
		return formatTrafficUnit(float64(size)/float64(trafficMB), "MB")
	case size >= trafficKB:
		return formatTrafficUnit(float64(size)/float64(trafficKB), "KB")
	default:
		return strconv.FormatInt(size, 10) + " B"
	}
}

func formatTrafficUnit(v float64, unit string) string {
	return strconv.FormatFloat(v, 'f', 2, 64) + " " + unit
}

// FormatTrafficLimit 格式化流量上限，与“流量值设置为 0 则不限制流量”的口径对应：
// 0（或负数）表示不限量。直接走 FormatTrafficSize 会显示成 “0 B”，容易被当成“一丝流量都没有”。
func FormatTrafficLimit(limit int64) string {
	if limit <= 0 {
		return "无限制"
	}
	return FormatTrafficSize(limit)
}
