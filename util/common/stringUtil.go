package common

// IsSubString 判断 target 是否为 str_array 的精确成员。
// 线性查找且不改动传入切片，避免 sort.Strings 原地排序调用方数据。
func IsSubString(target string, str_array []string) bool {
	for _, s := range str_array {
		if s == target {
			return true
		}
	}
	return false
}
