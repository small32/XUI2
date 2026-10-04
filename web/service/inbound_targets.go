package service

import (
	"encoding/json"
	"errors"
	"xui/database"
	"xui/database/model"
)

func ValidateInboundTargets(in *model.Inbound) error {
	if in.TargetNodes == "" {
		return nil
	}
	var ids []int
	if err := json.Unmarshal([]byte(in.TargetNodes), &ids); err != nil || ids == nil {
		return errors.New("请选择有效的被控端")
	}
	unique := make([]int, 0, len(ids))
	seen := map[int]bool{}
	for _, id := range ids {
		if id <= 0 {
			return errors.New("请选择有效的被控端")
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		var count int64
		if err := database.GetDB().Model(&model.ManagedNode{}).Where("id = ?", id).Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			return errors.New("选择的被控端已不存在，请刷新列表")
		}
		unique = append(unique, id)
	}
	data, _ := json.Marshal(unique)
	in.TargetNodes = string(data)
	return nil
}
