package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"sync"
	"xui/database"
	"xui/database/model"

	"gorm.io/gorm"
)

type NodeInboundList struct {
	NodeID   int             `json:"nodeId"`
	Name     string          `json:"name"`
	Inbounds []model.Inbound `json:"inbounds"`
	Error    string          `json:"error"`
}

type InboundCopyPayload struct {
	Inbound   model.Inbound `json:"inbound"`
	Overwrite bool          `json:"overwrite"`
}

type NodeInboundCopyRequest struct {
	SourceNodeID  int   `json:"sourceNodeId" form:"sourceNodeId"`
	TargetNodeIDs []int `json:"targetNodeIds" form:"targetNodeIds"`
	Ports         []int `json:"ports" form:"ports"`
	Overwrite     bool  `json:"overwrite" form:"overwrite"`
}

type NodeInboundCopyResult struct {
	NodeID  int    `json:"nodeId"`
	Name    string `json:"name"`
	Port    int    `json:"port"`
	Success bool   `json:"success"`
	Error   string `json:"error"`
}

func (s *ServerManagementService) readNodeInbounds(node *model.ManagedNode) ([]model.Inbound, error) {
	var response struct {
		Inbounds []model.Inbound `json:"inbounds"`
	}
	err := s.call(node, http.MethodGet, "/inbounds/all", nil, "", &response)
	if err != nil {
		return nil, fmt.Errorf("读取全部入站失败，请确认被控端已更新：%w", err)
	}
	return response.Inbounds, nil
}

func (s *ServerManagementService) AllNodeInbounds() ([]NodeInboundList, error) {
	nodes, err := s.Nodes()
	if err != nil {
		return nil, err
	}
	result := make([]NodeInboundList, len(nodes))
	var wg sync.WaitGroup
	// Each node retains its own error so offline nodes do not hide other lists.
	for i, node := range nodes {
		wg.Add(1)
		go func(i int, node model.ManagedNode) {
			defer wg.Done()
			rows, err := s.readNodeInbounds(&node)
			result[i] = NodeInboundList{NodeID: node.Id, Name: node.Name, Inbounds: rows}
			if err != nil {
				result[i].Error = err.Error()
			}
		}(i, node)
	}
	wg.Wait()
	return result, nil
}

func (s *ServerManagementService) CopyNodeInbounds(request NodeInboundCopyRequest) ([]NodeInboundCopyResult, error) {
	if request.SourceNodeID <= 0 || len(request.TargetNodeIDs) == 0 || len(request.Ports) == 0 {
		return nil, errors.New("请选择来源被控端、入站和目标被控端")
	}
	var source model.ManagedNode
	if err := database.GetDB().First(&source, request.SourceNodeID).Error; err != nil {
		return nil, err
	}
	targets := make([]model.ManagedNode, 0, len(request.TargetNodeIDs))
	seen := map[int]bool{}
	for _, id := range request.TargetNodeIDs {
		if id == source.Id || id <= 0 {
			return nil, errors.New("目标被控端不能是来源被控端")
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		var target model.ManagedNode
		if err := database.GetDB().First(&target, id).Error; err != nil {
			return nil, err
		}
		targets = append(targets, target)
	}
	rows, err := s.readNodeInbounds(&source)
	if err != nil {
		return nil, err
	}
	byPort := map[int]model.Inbound{}
	for _, row := range rows {
		byPort[row.Port] = row
	}
	selected := make([]model.Inbound, 0, len(request.Ports))
	seen = map[int]bool{}
	for _, port := range request.Ports {
		in, exists := byPort[port]
		if !exists {
			return nil, fmt.Errorf("来源被控端不存在端口 %d，请刷新列表", port)
		}
		if seen[port] {
			continue
		}
		seen[port] = true
		in.Id, in.UserId, in.ManagerAccountID = 0, 0, 0
		in.ManagerRevision, in.Tag = "", ""
		in.Up, in.Down = 0, 0
		selected = append(selected, in)
	}
	result := make([]NodeInboundCopyResult, 0, len(targets)*len(selected))
	for _, target := range targets {
		for _, in := range selected {
			err := s.call(&target, http.MethodPut, "/inbounds/copy/"+strconv.Itoa(in.Port), InboundCopyPayload{in, request.Overwrite}, "", nil)
			row := NodeInboundCopyResult{NodeID: target.Id, Name: target.Name, Port: in.Port, Success: err == nil}
			if err != nil {
				row.Error = err.Error()
			}
			result = append(result, row)
		}
	}
	return result, nil
}

// Copies are local inbounds. Managed identities cannot be cloned because the
// manager's reconciliation would delete or replace them on its next heartbeat.
func ImportNodeInbound(in model.Inbound, overwrite bool) error {
	if in.Port < 1 || in.Port > 65535 || in.Protocol == "" || !json.Valid([]byte(in.Settings)) || !json.Valid([]byte(in.StreamSettings)) || (in.Sniffing != "" && !json.Valid([]byte(in.Sniffing))) {
		return errors.New("入站配置无效")
	}
	s := new(InboundService)
	if err := s.CheckInboundRemark(in.Remark); err != nil {
		return err
	}
	if err := s.ApplyPanelCertificates(&in); err != nil {
		return err
	}
	return database.GetDB().Transaction(func(tx *gorm.DB) error {
		var existing model.Inbound
		err := tx.Where("port = ?", in.Port).First(&existing).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		in.Id, in.UserId, in.ManagerAccountID = 0, 0, 0
		in.ManagerRevision = ""
		in.Up, in.Down = 0, 0
		in.Tag = "inbound-" + strconv.Itoa(in.Port)
		if err == nil {
			if existing.ManagerAccountID > 0 {
				return errors.New("同端口为受管入站，请通过管理端统一配置")
			}
			if !overwrite {
				return errors.New("目标端口已存在，已跳过")
			}
			in.Id, in.UserId = existing.Id, existing.UserId
			in.Up, in.Down = existing.Up, existing.Down
		} else {
			var owner model.User
			if err := tx.Where("username = ?", model.AdminUsername).First(&owner).Error; err != nil {
				return err
			}
			in.UserId = owner.Id
		}
		return tx.Save(&in).Error
	})
}
