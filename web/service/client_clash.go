package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"gopkg.in/yaml.v2"
	"net"
	"net/url"
	"strings"
	"xui/database/model"
)

func (s *ClientService) ClashConfig(in *model.Inbound, baseURL string) ([]byte, error) {
	nodes, err := new(ServerManagementService).Nodes()
	if err != nil {
		return nil, err
	}
	proxies := []map[string]interface{}{}
	names := []string{}
	usedNames := map[string]bool{"XUI代理": true, "DIRECT": true, "GLOBAL": true, "REJECT": true}
	for _, node := range nodes {
		if !node.Enabled || !in.TargetsNode(node.Id) {
			continue
		}
		desired, err := InboundForNode(in, node.Id)
		if err != nil {
			return nil, err
		}
		name := strings.TrimSpace(node.Name)
		if name == "" {
			name = fmt.Sprintf("节点 %d", node.Id)
		}
		if usedNames[name] {
			name = fmt.Sprintf("%s · %d", name, node.Id)
		}
		usedNames[name] = true
		p, err := clashProxy(desired, hostOnlyAddress(node.Address), name)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", node.Name, err)
		}
		proxies = append(proxies, p)
		names = append(names, name)
	}
	if len(proxies) == 0 {
		return nil, errors.New("账号没有可用的下发节点")
	}
	rules := []string{}
	if u, err := url.Parse(baseURL); err == nil && u.Hostname() != "" {
		host := u.Hostname()
		if ip := net.ParseIP(host); ip != nil {
			if ip.To4() != nil {
				rules = append(rules, "IP-CIDR,"+host+"/32,DIRECT,no-resolve")
			}
		} else {
			rules = append(rules, "DOMAIN,"+host+",DIRECT")
		}
	}
	rules = append(rules, "IP-CIDR,127.0.0.0/8,DIRECT,no-resolve", "IP-CIDR,10.0.0.0/8,DIRECT,no-resolve", "IP-CIDR,172.16.0.0/12,DIRECT,no-resolve", "IP-CIDR,192.168.0.0/16,DIRECT,no-resolve", "GEOIP,CN,DIRECT", "MATCH,XUI代理")
	c := map[string]interface{}{"mixed-port": 7890, "allow-lan": false, "mode": "rule", "ipv6": false, "proxies": proxies, "proxy-groups": []map[string]interface{}{{"name": "XUI代理", "type": "select", "proxies": names}}, "rules": rules, "tun": map[string]interface{}{"enable": false, "stack": "mixed", "auto-route": true, "auto-detect-interface": true, "dns-hijack": []string{"any:53"}}, "dns": map[string]interface{}{"enable": true, "ipv6": false, "enhanced-mode": "fake-ip", "fake-ip-range": "198.18.0.1/16", "default-nameserver": []string{"223.5.5.5", "1.1.1.1"}, "nameserver": []string{"https://dns.alidns.com/dns-query", "https://cloudflare-dns.com/dns-query"}}}
	return yaml.Marshal(c)
}
func clashObject(raw string) (map[string]interface{}, error) {
	m := map[string]interface{}{}
	if strings.TrimSpace(raw) == "" {
		return m, nil
	}
	err := json.Unmarshal([]byte(raw), &m)
	return m, err
}
func clashString(m map[string]interface{}, key string) string { v, _ := m[key].(string); return v }
func clashMap(m map[string]interface{}, key string) map[string]interface{} {
	v, _ := m[key].(map[string]interface{})
	if v == nil {
		return map[string]interface{}{}
	}
	return v
}
func clashFirst(m map[string]interface{}, key string) map[string]interface{} {
	v, _ := m[key].([]interface{})
	if len(v) == 0 {
		return map[string]interface{}{}
	}
	r, _ := v[0].(map[string]interface{})
	return r
}
func clashProxy(in *model.Inbound, address, name string) (map[string]interface{}, error) {
	if address == "" {
		return nil, errors.New("请设置被控端订阅地址")
	}
	if ip := net.ParseIP(address); ip != nil && ip.To4() == nil {
		return nil, errors.New("当前客户端不支持 IPv6 节点地址")
	}
	settings, err := clashObject(in.Settings)
	if err != nil {
		return nil, fmt.Errorf("入站设置无效: %w", err)
	}
	stream, err := clashObject(in.StreamSettings)
	if err != nil {
		return nil, fmt.Errorf("传输设置无效: %w", err)
	}
	p := map[string]interface{}{"name": name, "server": address, "port": in.Port, "udp": true}
	client := clashFirst(settings, "clients")
	switch in.Protocol {
	case model.Shadowsocks:
		p["type"] = "ss"
		p["cipher"] = clashString(settings, "method")
		p["password"] = clashString(settings, "password")
		if p["cipher"] == "" || p["password"] == "" {
			return nil, errors.New("Shadowsocks 缺少加密方式或密码")
		}
	case model.Trojan:
		p["type"] = "trojan"
		p["password"] = clashString(client, "password")
		if p["password"] == "" {
			return nil, errors.New("Trojan 缺少密码")
		}
	case model.VMess, model.VLESS:
		p["type"] = string(in.Protocol)
		p["uuid"] = clashString(client, "id")
		if p["uuid"] == "" {
			return nil, errors.New("缺少 UUID")
		}
		if in.Protocol == model.VMess {
			p["alterId"] = 0
			if v, ok := client["alterId"]; ok {
				p["alterId"] = v
			}
			p["cipher"] = "auto"
		}
		if flow := clashString(client, "flow"); flow != "" {
			if flow != "xtls-rprx-vision" {
				return nil, errors.New("不支持的 VLESS flow: " + flow)
			}
			p["flow"] = flow
		}
	case model.Socks, model.Http:
		p["type"] = string(in.Protocol)
		if in.Protocol == model.Socks {
			p["type"] = "socks5"
		}
		user := clashFirst(settings, "accounts")
		if clashString(user, "user") != "" {
			p["username"] = clashString(user, "user")
			p["password"] = clashString(user, "pass")
		}
	default:
		return nil, errors.New("Clash 不支持此入站协议: " + string(in.Protocol))
	}
	security := clashString(stream, "security")
	if in.Protocol == model.Trojan && security != "tls" {
		return nil, errors.New("Trojan 客户端需要 TLS 配置")
	}
	if in.Protocol == model.Shadowsocks && security != "" && security != "none" {
		return nil, errors.New("Shadowsocks 节点不支持此 TLS 传输配置")
	}
	switch security {
	case "", "none":
	case "tls":
		p["tls"] = true
		tls := clashMap(stream, "tlsSettings")
		if sni := clashString(tls, "serverName"); sni != "" {
			p["servername"] = sni
			if in.Protocol == model.Trojan {
				p["sni"] = sni
			}
		}
		if v, ok := tls["allowInsecure"].(bool); ok {
			p["skip-cert-verify"] = v
		}
		if alpn, ok := tls["alpn"]; ok {
			p["alpn"] = alpn
		}
	default:
		return nil, errors.New("当前订阅转换不支持安全类型: " + security)
	}
	network := clashString(stream, "network")
	if network == "" {
		network = "tcp"
	}
	if (in.Protocol == model.Shadowsocks || in.Protocol == model.Socks || in.Protocol == model.Http) && network != "tcp" {
		return nil, errors.New("此协议不支持所配置的传输方式: " + network)
	}
	switch network {
	case "tcp":
		header := clashMap(clashMap(stream, "tcpSettings"), "header")
		if kind := clashString(header, "type"); kind != "" && kind != "none" {
			return nil, errors.New("不支持 TCP HTTP 伪装")
		}
	case "ws":
		p["network"] = "ws"
		ws := clashMap(stream, "wsSettings")
		opts := map[string]interface{}{"path": clashString(ws, "path")}
		if headers, ok := ws["headers"]; ok {
			opts["headers"] = headers
		}
		p["ws-opts"] = opts
	case "grpc":
		p["network"] = "grpc"
		p["grpc-opts"] = map[string]interface{}{"grpc-service-name": clashString(clashMap(stream, "grpcSettings"), "serviceName")}
	case "h2", "http":
		if in.Protocol != model.VMess && in.Protocol != model.VLESS {
			return nil, errors.New("此协议不支持 HTTP/2 传输")
		}
		p["network"] = "h2"
		h2 := clashMap(stream, "httpSettings")
		p["h2-opts"] = map[string]interface{}{"path": clashString(h2, "path"), "host": h2["host"]}
	default:
		return nil, errors.New("当前订阅转换不支持传输方式: " + network)
	}
	return p, nil
}
