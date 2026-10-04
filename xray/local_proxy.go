package xray

import (
	"encoding/json"
	"fmt"
)

const LocalHTTPTag = "xui-local-http"
const LocalSOCKSTag = "xui-local-socks"
const LocalHTTPPort = 10809
const LocalSOCKSPort = 10808

// BuildLocalProxyTemplate adds loopback proxy listeners while retaining the user's outbound and routing configuration.
// Only XUI's local listeners and their legacy routing overrides are replaced.
func BuildLocalProxyTemplate(template string, enabled bool) (string, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal([]byte(template), &root); err != nil || root == nil {
		return "", fmt.Errorf("Xray 配置模版必须是 JSON 对象")
	}
	var inbounds []map[string]interface{}
	if raw := root["inbounds"]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &inbounds); err != nil {
			return "", err
		}
	}
	kept := make([]map[string]interface{}, 0, len(inbounds))
	changed := false
	for _, in := range inbounds {
		tag, _ := in["tag"].(string)
		if tag == LocalHTTPTag || tag == LocalSOCKSTag {
			changed = true
			continue
		}
		kept = append(kept, in)
	}
	var routing map[string]json.RawMessage
	if raw := root["routing"]; len(raw) > 0 {
		if err := json.Unmarshal(raw, &routing); err != nil {
			return "", err
		}
	}
	if routing != nil {
		var rules []map[string]interface{}
		if raw := routing["rules"]; len(raw) > 0 {
			if err := json.Unmarshal(raw, &rules); err != nil {
				return "", err
			}
		}
		result := make([]map[string]interface{}, 0, len(rules))
		rulesChanged := false
		for _, rule := range rules {
			if tags, ok := rule["inboundTag"].([]interface{}); ok {
				remaining := make([]interface{}, 0, len(tags))
				removed := false
				for _, tag := range tags {
					if tag == LocalHTTPTag || tag == LocalSOCKSTag {
						removed = true
						continue
					}
					remaining = append(remaining, tag)
				}
				if removed {
					rulesChanged = true
					if len(remaining) == 0 {
						continue
					}
					rule["inboundTag"] = remaining
				}
			}
			result = append(result, rule)
		}
		if rulesChanged {
			changed = true
			routing["rules"], _ = json.Marshal(result)
			root["routing"], _ = json.Marshal(routing)
		}
	}
	if enabled {
		var outbounds []map[string]interface{}
		if err := json.Unmarshal(root["outbounds"], &outbounds); err != nil {
			return "", fmt.Errorf("请先在 Xray 配置模版中填写上级出站及路由")
		}
		proxy := false
		for _, out := range outbounds {
			p, _ := out["protocol"].(string)
			if p != "" && p != "freedom" && p != "blackhole" && p != "dns" {
				proxy = true
			}
		}
		if !proxy {
			return "", fmt.Errorf("请先在 Xray 配置模版中填写上级代理出站及路由")
		}
		for _, in := range kept {
			if port, ok := in["port"].(float64); ok && (int(port) == LocalHTTPPort || int(port) == LocalSOCKSPort) {
				return "", fmt.Errorf("本机代理端口 10808/10809 与配置模版的现有入站冲突")
			}
		}
		kept = append(kept, map[string]interface{}{"tag": LocalHTTPTag, "listen": "127.0.0.1", "port": LocalHTTPPort, "protocol": "http", "settings": map[string]interface{}{}}, map[string]interface{}{"tag": LocalSOCKSTag, "listen": "127.0.0.1", "port": LocalSOCKSPort, "protocol": "socks", "settings": map[string]interface{}{"auth": "noauth", "udp": true}})
		changed = true
	}
	if !changed {
		return template, nil
	}
	root["inbounds"], _ = json.Marshal(kept)
	data, err := json.MarshalIndent(root, "", "  ")
	return string(data), err
}
