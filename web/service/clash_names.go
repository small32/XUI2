package service

import (
	"fmt"
	"regexp"
)

var nodeNamePattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// These names are supplied by Clash/Mihomo itself and cannot be proxy names.
var clashReservedNames = map[string]bool{
	"DIRECT": true, "REJECT": true, "REJECT-DROP": true,
	"COMPATIBLE": true, "PASS": true, "PASS-RULE": true, "GLOBAL": true,
}

func validNodeName(name string) bool {
	return nodeNamePattern.MatchString(name) && !clashReservedNames[name]
}

func clashNameSet(groups ...string) map[string]bool {
	used := make(map[string]bool, len(clashReservedNames)+len(groups))
	for name := range clashReservedNames {
		used[name] = true
	}
	for _, name := range groups {
		used[name] = true
	}
	return used
}

// Legacy names may already contain the suffix chosen for another node. Keep
// trying until the name is free, without changing the stored node or remark.
func claimClashName(used map[string]bool, name string, id int) string {
	candidate := name
	if used[candidate] {
		base := fmt.Sprintf("%s · %d", name, id)
		candidate = base
		for n := 2; used[candidate]; n++ {
			candidate = fmt.Sprintf("%s (%d)", base, n)
		}
	}
	used[candidate] = true
	return candidate
}
