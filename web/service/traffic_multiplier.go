package service

import (
	"errors"
	"math"
	"xui/database/model"
)

func validateTrafficMultiplier(value *float64) error {
	if value != nil && (*value < 0 || math.IsNaN(*value) || math.IsInf(*value, 0)) {
		return errors.New("流量倍率必须是非负的有限数字")
	}
	return nil
}

// sumTraffic keeps aggregate usage representable without wrapping a valid
// nonnegative total below zero. Existing negative counters remain signed;
// this only saturates arithmetic overflow and never rewrites raw readings.
func sumTraffic(values ...int64) int64 {
	var total int64
	for _, value := range values {
		if value > 0 && total > math.MaxInt64-value {
			total = math.MaxInt64
		} else if value < 0 && total < math.MinInt64-value {
			total = math.MinInt64
		} else {
			total += value
		}
	}
	return total
}

func remainingTraffic(limit, used int64) int64 {
	if used >= limit {
		return 0
	}
	if used < 0 && limit > math.MaxInt64+used {
		return math.MaxInt64
	}
	return limit - used
}

func scaledNodeTraffic(bytes int64, node model.ManagedNode) int64 {
	if node.TrafficMultiplier == nil || *node.TrafficMultiplier == 1 {
		return bytes
	}
	value := math.Round(float64(bytes) * *node.TrafficMultiplier)
	if value >= math.MaxInt64 {
		return math.MaxInt64
	}
	if value <= math.MinInt64 {
		return math.MinInt64
	}
	return int64(value)
}

// Keep agent counters raw in the database. Apply the current multiplier only
// to calculation copies, so edits and repeated polls never compound usage.
func scaleNodeReadings(rows []model.NodeTraffic, nodes []model.ManagedNode) {
	byID := make(map[int]model.ManagedNode, len(nodes))
	for _, node := range nodes {
		byID[node.Id] = node
	}
	for i := range rows {
		node := byID[rows[i].NodeID]
		rows[i].Up = scaledNodeTraffic(rows[i].Up, node)
		rows[i].Down = scaledNodeTraffic(rows[i].Down, node)
	}
}

func nodeTrafficMultiplier(node model.ManagedNode) float64 {
	if node.TrafficMultiplier == nil {
		return 1
	}
	return *node.TrafficMultiplier
}
