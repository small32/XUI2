package service

import (
	"encoding/json"
	"fmt"
	"x-ui/database/model"
)

// WithLoginPassword only changes response data, never persisted credentials.
func WithLoginPassword(protocol model.Protocol, raw, password string) (string, error) {
	var settings map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &settings); err != nil || settings == nil {
		return "", fmt.Errorf("入站配置无效")
	}
	switch protocol {
	case model.Shadowsocks:
		settings["password"] = password
	case model.Trojan, model.Socks, model.Http:
		key, field := "clients", "password"
		if protocol != model.Trojan {
			key, field = "accounts", "pass"
		}
		items, ok := settings[key].([]interface{})
		if !ok || len(items) == 0 {
			return "", fmt.Errorf("入站账号配置无效")
		}
		for _, item := range items {
			account, ok := item.(map[string]interface{})
			if !ok {
				return "", fmt.Errorf("入站账号配置无效")
			}
			account[field] = password
		}
	default:
		return "", fmt.Errorf("入站协议已变化，请退出后重新登录")
	}
	b, err := json.Marshal(settings)
	return string(b), err
}
