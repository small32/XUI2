package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"xui/database"
	"xui/database/model"
)

func (s *ServerManagementService) sendNodeInbound(ctx context.Context, node *model.ManagedNode, in *model.Inbound, operation string, out interface{}) error {
	desired, err := InboundForNode(in, node.Id)
	if err != nil {
		return err
	}
	desired.ManagerAccountID = in.Id
	desired.ManagerRevision = inboundRevision(desired)
	desired.NodeConfigs, desired.TargetNodes, desired.SharedPassword = "", "", ""
	return s.callContext(ctx, node, http.MethodPut, "/inbounds/"+strconv.Itoa(desired.Port), desired, operation, out)
}

type NodeInboundConfig struct {
	Protocol       model.Protocol `json:"protocol"`
	Settings       string         `json:"settings"`
	StreamSettings string         `json:"streamSettings"`
	Sniffing       string         `json:"sniffing"`
	Listen         string         `json:"listen"`
}

func sharedProtocolPassword(in *model.Inbound) string {
	if in.SharedPassword != "" {
		return in.SharedPassword
	}
	return inboundPassword(in)
}

func applySharedPassword(in *model.Inbound, password string) error {
	if in.Protocol != model.Trojan && in.Protocol != model.Shadowsocks && in.Protocol != model.Socks && in.Protocol != model.Http {
		return nil
	}
	if password == "" {
		return errors.New("带密码的协议需要填写账号统一密码")
	}
	var settings map[string]interface{}
	if err := json.Unmarshal([]byte(in.Settings), &settings); err != nil || settings == nil {
		return errors.New("协议设置必须是 JSON 对象")
	}
	switch in.Protocol {
	case model.Shadowsocks:
		settings["password"] = password
	case model.Trojan:
		clients, ok := settings["clients"].([]interface{})
		if !ok || len(clients) == 0 {
			return errors.New("Trojan 至少需要一个客户端")
		}
		for _, raw := range clients {
			client, ok := raw.(map[string]interface{})
			if !ok {
				return errors.New("Trojan 客户端配置无效")
			}
			client["password"] = password
		}
	case model.Socks, model.Http:
		accounts, ok := settings["accounts"].([]interface{})
		if !ok || len(accounts) == 0 {
			return errors.New("HTTP/SOCKS 需要配置密码账号")
		}
		for _, raw := range accounts {
			account, ok := raw.(map[string]interface{})
			if !ok {
				return errors.New("密码账号配置无效")
			}
			account["pass"] = password
		}
		if in.Protocol == model.Socks {
			settings["auth"] = "password"
		}
	}
	data, err := json.Marshal(settings)
	in.Settings = string(data)
	return err
}

func InboundForNode(in *model.Inbound, nodeID int) (*model.Inbound, error) {
	copy := *in
	if in.NodeConfigs != "" {
		var configs map[string]NodeInboundConfig
		if err := json.Unmarshal([]byte(in.NodeConfigs), &configs); err != nil {
			return nil, err
		}
		if c, ok := configs[strconv.Itoa(nodeID)]; ok {
			copy.Protocol = c.Protocol
			copy.Settings = c.Settings
			copy.StreamSettings = c.StreamSettings
			copy.Sniffing = c.Sniffing
			copy.Listen = c.Listen
		}
	}
	password := sharedProtocolPassword(in)
	if password != "" || in.NodeConfigs != "" && in.NodeConfigs != "{}" {
		if err := applySharedPassword(&copy, password); err != nil {
			return nil, err
		}
	}
	return &copy, nil
}

func nodeInboundRevision(in *model.Inbound, nodeID int) string {
	desired, err := InboundForNode(in, nodeID)
	if err != nil {
		return ""
	}
	return inboundRevision(desired)
}

func ValidateNodeProtocols(in *model.Inbound) error {
	in.SharedPassword = sharedProtocolPassword(in)
	if in.SharedPassword != "" {
		if err := applySharedPassword(in, in.SharedPassword); err != nil {
			return err
		}
	}
	if in.NodeConfigs == "" {
		return nil
	}
	var configs map[string]NodeInboundConfig
	if err := json.Unmarshal([]byte(in.NodeConfigs), &configs); err != nil || configs == nil {
		return errors.New("节点协议配置无效")
	}
	for key, c := range configs {
		id, err := strconv.Atoi(key)
		if err != nil || id <= 0 {
			return errors.New("节点协议配置的节点 ID 无效")
		}
		var count int64
		if err := database.GetDB().Model(&model.ManagedNode{}).Where("id = ?", id).Count(&count).Error; err != nil {
			return err
		}
		if count == 0 {
			return errors.New("协议配置中的被控端已不存在")
		}
		switch c.Protocol {
		case model.VMess, model.VLESS, model.Trojan, model.Shadowsocks, model.Socks, model.Http, model.Dokodemo:
		default:
			return fmt.Errorf("节点 %d 的协议无效", id)
		}
		if !json.Valid([]byte(c.Settings)) || !json.Valid([]byte(c.StreamSettings)) || !json.Valid([]byte(c.Sniffing)) {
			return fmt.Errorf("节点 %d 的连接设置无效", id)
		}
		if _, err := InboundForNode(in, id); err != nil {
			return fmt.Errorf("节点 %d: %w", id, err)
		}
	}
	return nil
}
