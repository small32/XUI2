package xray

import (
	"encoding/json"
	"fmt"
	"net"
	"strconv"
	"strings"
)

const UpstreamTag = "xui-upstream"
const LocalHTTPTag = "xui-local-http"
const LocalSOCKSTag = "xui-local-socks"
const TunnelTag = "xui-upstream-tunnel"
const LocalHTTPPort = 10809
const LocalSOCKSPort = 10808
const TunnelPort = 10810

// Upstream describes the remote connection, independently of local inbounds.
type Upstream struct {
	Protocol       string          `json:"protocol"`
	Address        string          `json:"address"`
	Port           int             `json:"port"`
	Password       string          `json:"password"`
	ID             string          `json:"id"`
	AlterID        int             `json:"alterId"`
	Encryption     string          `json:"encryption"`
	Flow           string          `json:"flow"`
	StreamSettings json.RawMessage `json:"streamSettings"`
}

func ParseUpstream(value string) (*Upstream, error) {
	var u Upstream
	if err := json.Unmarshal([]byte(value), &u); err != nil {
		return nil, fmt.Errorf("上级服务器配置无效: %w", err)
	}
	u.Address = strings.TrimSpace(u.Address)
	if u.Address == "" || strings.ContainsAny(u.Address, " /\r\n\t") || u.Port < 1 || u.Port > 65535 {
		return nil, fmt.Errorf("请填写有效的上级服务器地址和端口")
	}
	switch u.Protocol {
	case "trojan":
		if u.Password == "" {
			return nil, fmt.Errorf("Trojan 上级密码不能为空")
		}
	case "vmess", "vless":
		if u.ID == "" {
			return nil, fmt.Errorf("VMess/VLESS 上级 UUID 不能为空")
		}
		if u.AlterID < 0 {
			return nil, fmt.Errorf("alterId 不能小于 0")
		}
	case "dokodemo-door":
	default:
		return nil, fmt.Errorf("不支持的上级协议: %s", u.Protocol)
	}
	if len(u.StreamSettings) == 0 {
		u.StreamSettings = json.RawMessage(`{}`)
	}
	var stream map[string]json.RawMessage
	if err := json.Unmarshal(u.StreamSettings, &stream); err != nil || stream == nil {
		return nil, fmt.Errorf("上级传输配置必须是 JSON 对象")
	}
	return &u, nil
}

func (u *Upstream) outbound() map[string]interface{} {
	o := map[string]interface{}{"tag": UpstreamTag, "protocol": u.Protocol, "streamSettings": u.StreamSettings}
	switch u.Protocol {
	case "trojan":
		o["settings"] = map[string]interface{}{"servers": []interface{}{map[string]interface{}{"address": u.Address, "port": u.Port, "password": u.Password}}}
	case "vmess", "vless":
		user := map[string]interface{}{"id": u.ID}
		if u.Protocol == "vmess" {
			user["alterId"] = u.AlterID
			user["security"] = "auto"
		} else {
			user["encryption"] = u.Encryption
			if u.Encryption == "" {
				user["encryption"] = "none"
			}
			if u.Flow != "" {
				user["flow"] = u.Flow
			}
		}
		o["settings"] = map[string]interface{}{"vnext": []interface{}{map[string]interface{}{"address": u.Address, "port": u.Port, "users": []interface{}{user}}}}
	case "dokodemo-door":
		// Dokodemo-door is an inbound tunnel, not an outbound proxy protocol.
		o["protocol"] = "freedom"
		o["settings"] = map[string]interface{}{"redirect": net.JoinHostPort(u.Address, strconv.Itoa(u.Port))}
	}
	return o
}

func managedTag(tag string) bool {
	return tag == UpstreamTag || tag == LocalHTTPTag || tag == LocalSOCKSTag || tag == TunnelTag
}

// BuildUpstreamTemplate replaces only panel-owned entries, preserving user rules and API settings.
func BuildUpstreamTemplate(template, upstream string, enabled, local bool) (string, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal([]byte(template), &root); err != nil || root == nil {
		return "", fmt.Errorf("Xray 配置模版必须是 JSON 对象")
	}
	var u *Upstream
	var err error
	if enabled {
		u, err = ParseUpstream(upstream)
		if err != nil {
			return "", err
		}
	}
	if local && (!enabled || u.Protocol == "dokodemo-door") {
		return "", fmt.Errorf("本机代理需要启用 Trojan、VMess 或 VLESS 上级；dokodemo-door 仅支持固定目标转发")
	}
	decodeList := func(key string) ([]map[string]interface{}, error) {
		var list []map[string]interface{}
		if raw := root[key]; len(raw) > 0 {
			if err := json.Unmarshal(raw, &list); err != nil {
				return nil, fmt.Errorf("%s 配置无效: %w", key, err)
			}
		}
		out := make([]map[string]interface{}, 0, len(list))
		for _, item := range list {
			if tag, _ := item["tag"].(string); !managedTag(tag) {
				out = append(out, item)
			}
		}
		return out, nil
	}
	inbounds, err := decodeList("inbounds")
	if err != nil {
		return "", err
	}
	outbounds, err := decodeList("outbounds")
	if err != nil {
		return "", err
	}
	var routing map[string]json.RawMessage
	if raw := root["routing"]; len(raw) > 0 {
		if err = json.Unmarshal(raw, &routing); err != nil {
			return "", err
		}
	}
	if routing == nil {
		routing = map[string]json.RawMessage{}
	}
	var rules []map[string]interface{}
	if raw := routing["rules"]; len(raw) > 0 {
		if err = json.Unmarshal(raw, &rules); err != nil {
			return "", err
		}
	}
	kept := make([]map[string]interface{}, 0, len(rules))
	for _, r := range rules {
		tag, _ := r["outboundTag"].(string)
		if !managedTag(tag) {
			kept = append(kept, r)
		}
	}
	addInbound := func(tag, protocol string, port int, settings interface{}) error {
		for _, in := range inbounds {
			if p, ok := in["port"].(float64); ok && int(p) == port {
				return fmt.Errorf("模版端口 %d 已占用，请修改现有端口后重试", port)
			}
		}
		inbounds = append(inbounds, map[string]interface{}{"tag": tag, "listen": "127.0.0.1", "port": port, "protocol": protocol, "settings": settings})
		return nil
	}
	if enabled {
		outbounds = append(outbounds, u.outbound())
		if u.Protocol == "dokodemo-door" {
			if err = addInbound(TunnelTag, "dokodemo-door", TunnelPort, map[string]interface{}{"address": u.Address, "port": u.Port, "network": "tcp,udp"}); err != nil {
				return "", err
			}
			kept = append([]map[string]interface{}{{"type": "field", "inboundTag": []string{TunnelTag}, "outboundTag": UpstreamTag}}, kept...)
		} else {
			// Keep existing API and blocking rules ahead of the fallback; replace broad routing with the selected upstream.
			fallback := map[string]interface{}{"type": "field", "network": "tcp,udp", "outboundTag": UpstreamTag}
			pos := len(kept)
			for i, r := range kept {
				if _, ok := r["network"]; ok && len(r) == 3 && r["type"] == "field" {
					pos = i
					break
				}
			}
			kept = append(kept, nil)
			copy(kept[pos+1:], kept[pos:])
			kept[pos] = fallback
			if local {
				if err = addInbound(LocalHTTPTag, "http", LocalHTTPPort, map[string]interface{}{}); err != nil {
					return "", err
				}
				if err = addInbound(LocalSOCKSTag, "socks", LocalSOCKSPort, map[string]interface{}{"auth": "noauth", "udp": true}); err != nil {
					return "", err
				}
				kept = append([]map[string]interface{}{{"type": "field", "inboundTag": []string{LocalHTTPTag, LocalSOCKSTag}, "outboundTag": UpstreamTag}}, kept...)
			}
		}
	}
	root["inbounds"], _ = json.Marshal(inbounds)
	root["outbounds"], _ = json.Marshal(outbounds)
	routing["rules"], _ = json.Marshal(kept)
	root["routing"], _ = json.Marshal(routing)
	data, err := json.MarshalIndent(root, "", "  ")
	return string(data), err
}
