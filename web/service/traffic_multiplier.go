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

func scaledNodeTraffic(bytes int64, node model.ManagedNode) int64 {
	if node.TrafficMultiplier == nil || *node.TrafficMultiplier == 1 {
		return bytes
	}
	value := math.Round(float64(bytes) * *node.TrafficMultiplier)
	if value >= math.MaxInt64 {
		return math.MaxInt64
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
