package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"xui/database/model"
)

var inboundPasswordPattern = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
var ErrInboundPassword = errors.New("入站密码只能包含大小写字母、数字、下划线和连字符，且不能为空")

// Validate only newly authored credentials. An unchanged stored password must
// remain usable when editing unrelated fields or replaying the same config.
func validateInboundPasswords(in, previous *model.Inbound) error {
	current, err := inboundPasswordFields(in)
	if err != nil {
		return err
	}
	old := map[string]string{}
	oldShared := ""
	if previous != nil {
		old, _ = inboundPasswordFields(previous)
		oldShared = sharedProtocolPassword(previous)
	}
	shared := sharedProtocolPassword(in)
	for field, password := range current {
		if inboundPasswordPattern.MatchString(password) {
			continue
		}
		if previous != nil {
			if stored, exists := old[field]; exists && stored == password {
				continue
			}
			// Protocol overrides inherit the account's existing shared password.
			if password != "" && password == shared && password == oldShared {
				continue
			}
		}
		return fmt.Errorf("%s: %w", field, ErrInboundPassword)
	}
	return nil
}

func inboundPasswordFields(in *model.Inbound) (map[string]string, error) {
	fields := map[string]string{}
	if in.SharedPassword != "" {
		fields["账号统一密码"] = in.SharedPassword
	}
	if err := appendProtocolPasswords(fields, "默认协议", in); err != nil {
		return nil, err
	}
	if in.NodeConfigs != "" {
		var configs map[string]NodeInboundConfig
		if err := json.Unmarshal([]byte(in.NodeConfigs), &configs); err != nil {
			return nil, fmt.Errorf("节点协议配置无效")
		}
		for key := range configs {
			id, err := strconv.Atoi(key)
			if err != nil || id <= 0 {
				return nil, fmt.Errorf("节点协议配置的节点 ID 无效")
			}
			desired, err := InboundForNode(in, id)
			if err != nil {
				return nil, err
			}
			if err := appendProtocolPasswords(fields, "节点 "+key, desired); err != nil {
				return nil, err
			}
		}
	}
	return fields, nil
}

func appendProtocolPasswords(fields map[string]string, prefix string, in *model.Inbound) error {
	switch in.Protocol {
	case model.Shadowsocks, model.Trojan, model.Socks, model.Http:
	default:
		return nil
	}
	var settings map[string]interface{}
	if err := json.Unmarshal([]byte(in.Settings), &settings); err != nil || settings == nil {
		return fmt.Errorf("%s的密码配置无效", prefix)
	}
	if in.Protocol == model.Socks && settings["auth"] != "password" {
		return nil
	}
	if in.Protocol == model.Shadowsocks {
		raw, exists := settings["password"]
		password, ok := raw.(string)
		if exists && !ok {
			return fmt.Errorf("%s: %w", prefix, ErrInboundPassword)
		}
		fields[prefix+"密码"] = password
		return nil
	}
	key, passwordKey := "clients", "password"
	if in.Protocol != model.Trojan {
		key, passwordKey = "accounts", "pass"
	}
	items, _ := settings[key].([]interface{})
	for i, item := range items {
		account, ok := item.(map[string]interface{})
		if !ok {
			return fmt.Errorf("%s的密码账号配置无效", prefix)
		}
		raw, exists := account[passwordKey]
		password, ok := raw.(string)
		if exists && !ok {
			return fmt.Errorf("%s: %w", prefix, ErrInboundPassword)
		}
		fields[fmt.Sprintf("%s账号%d密码", prefix, i+1)] = password
	}
	return nil
}
