package service

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"unicode"

	"gopkg.in/yaml.v2"
	"xui/config"
	"xui/database/model"
)

// 订阅分组名与 ACL4SSR Mini 方案保持一致，规则文件里的组名直接对应这里的策略组。
const (
	subGroupProxy  = "🚀 节点选择"
	subGroupAuto   = "♻️ 自动选择"
	subGroupDirect = "🎯 全球直连"
	subGroupBlock  = "🛑 全球拦截"
	subGroupFinal  = "🐟 漏网之鱼"
)

type subRule struct {
	Type  string
	Value string
	Extra string
	Group string
}

// clashRuleTypes 是 Clash（Mihomo）真正支持的规则类型。ACL4SSR 规则集里混着
// Surge 专用的 URL-REGEX，带进去会让客户端整体校验失败（unsupported rule type）。
var clashRuleTypes = map[string]bool{
	"DOMAIN":         true,
	"DOMAIN-SUFFIX":  true,
	"DOMAIN-KEYWORD": true,
	"DOMAIN-REGEX":   true,
	"GEOIP":          true,
	"IP-CIDR":        true,
	"IP-CIDR6":       true,
	"SRC-IP-CIDR":    true,
	"SRC-PORT":       true,
	"DST-PORT":       true,
	"PROCESS-NAME":   true,
	"PROCESS-PATH":   true,
	"MATCH":          true,
}

var (
	subRuleMu     sync.Mutex
	subRules      []subRule
	subRulesReady bool
	subRuleBytes  []byte
)

// SetSubscriptionRules 由面板启动时注入规则文件内容（随静态资源一起打包），
// 规则文件格式沿用 ACL4SSR Mini：每行 ruleset=组名,[]规则。
func SetSubscriptionRules(data []byte) {
	subRuleMu.Lock()
	defer subRuleMu.Unlock()
	subRuleBytes = data
	subRules = nil
	subRulesReady = false
}

func loadSubRules() ([]subRule, error) {
	subRuleMu.Lock()
	defer subRuleMu.Unlock()
	if subRulesReady {
		return subRules, nil
	}
	if len(subRuleBytes) == 0 {
		// 不缓存这个失败：规则可能稍后才注入，缓存了会让订阅永久 500。
		return nil, errors.New("订阅规则未加载")
	}
	rules := make([]subRule, 0, 4096)
	for _, line := range strings.Split(string(subRuleBytes), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, ";") || strings.HasPrefix(line, "#") ||
			strings.HasPrefix(line, "[") {
			continue
		}
		if !strings.HasPrefix(line, "ruleset=") {
			continue
		}
		body := strings.TrimPrefix(line, "ruleset=")
		sep := strings.Index(body, ",")
		if sep <= 0 {
			continue
		}
		group := body[:sep]
		rule := strings.TrimPrefix(body[sep+1:], "[]")
		parts := strings.Split(rule, ",")
		if len(parts) == 0 || parts[0] == "" {
			continue
		}
		r := subRule{Group: group}
		if parts[0] == "FINAL" || parts[0] == "MATCH" {
			// 兜底规则没有匹配值，输出为 MATCH,<策略组>
			r.Type = "MATCH"
		} else {
			if len(parts) < 2 {
				continue
			}
			r.Type, r.Value = parts[0], parts[1]
			if len(parts) > 2 {
				r.Extra = strings.Join(parts[2:], ",")
			}
		}
		// 跳过 Clash 不认识的规则类型，否则整份配置会校验失败
		if !clashRuleTypes[r.Type] {
			continue
		}
		// 组名必须是下面这五个策略组之一：规则指向不存在的组，Clash 会拒绝整份配置
		if !knownSubGroup(r.Group) {
			continue
		}
		rules = append(rules, r)
	}
	if len(rules) == 0 {
		return nil, errors.New("订阅规则为空")
	}
	subRules = rules
	subRulesReady = true
	return subRules, nil
}

// SubscriptionService 在面板本地生成客户端订阅，不经过任何外部转换服务。
type SubscriptionService struct{}

// ClashConfig 生成 Clash 订阅：节点取自被控端列表（管理端）或本机入站（被控端）。
// fallbackHost 用于被控端：客户端从哪个地址取订阅，节点就用哪个地址。
func (s *SubscriptionService) ClashConfig(in *model.Inbound, fallbackHost string) ([]byte, error) {
	rules, err := loadSubRules()
	if err != nil {
		return nil, err
	}
	proxies, names, err := clashProxies(in, fallbackHost)
	if err != nil {
		return nil, err
	}
	if len(proxies) == 0 {
		return nil, errors.New("没有可用的节点")
	}
	groups := []map[string]interface{}{
		{"name": subGroupProxy, "type": "select", "proxies": append([]string{subGroupAuto, "DIRECT"}, names...)},
		{"name": subGroupAuto, "type": "url-test", "url": "http://www.gstatic.com/generate_204", "interval": 300, "tolerance": 50, "proxies": names},
		{"name": subGroupDirect, "type": "select", "proxies": []string{"DIRECT", subGroupProxy, subGroupAuto}},
		{"name": subGroupBlock, "type": "select", "proxies": []string{"REJECT", "DIRECT"}},
		{"name": subGroupFinal, "type": "select", "proxies": append([]string{subGroupProxy, subGroupDirect, subGroupAuto}, names...)},
	}
	out := make([]string, 0, len(rules))
	for _, r := range rules {
		line := r.Type
		if r.Value != "" {
			line += "," + r.Value
		}
		line += "," + r.Group
		if r.Extra != "" {
			line += "," + r.Extra
		}
		out = append(out, line)
	}
	cfg := map[string]interface{}{
		"mixed-port":   7890,
		"allow-lan":    true,
		"mode":         "rule",
		"log-level":    "info",
		"proxies":      proxies,
		"proxy-groups": groups,
		"rules":        out,
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return nil, err
	}
	return unescapeYAMLUnicode(data), nil
}

// yamlUnicodeEscape 匹配 yaml.v2 为非 BMP 字符输出的 \U 八位转义。
var yamlUnicodeEscape = regexp.MustCompile(`\\U([0-9A-Fa-f]{8})`)

// unescapeYAMLUnicode 把 \U0001F680 这类转义还原成真实字符。两种写法 Mihomo 都认，
// 但配置文件是给人排查的，满屏转义码不好读。
func unescapeYAMLUnicode(in []byte) []byte {
	return yamlUnicodeEscape.ReplaceAllFunc(in, func(m []byte) []byte {
		v, err := strconv.ParseUint(string(m[2:]), 16, 32)
		if err != nil || v > unicode.MaxRune {
			return m
		}
		return []byte(string(rune(v)))
	})
}

// knownSubGroup 判断规则里的策略组是不是面板生成的那几个，
// 规则文件被改成别的组名时，宁可丢掉这条规则也不能让客户端整份配置失效。
func knownSubGroup(name string) bool {
	switch name {
	case subGroupProxy, subGroupAuto, subGroupDirect, subGroupBlock, subGroupFinal:
		return true
	}
	return false
}

// clashProxies 汇总订阅里的节点：管理端按被控端逐个生成，被控端用本机地址。
func clashProxies(in *model.Inbound, fallbackHost string) ([]map[string]interface{}, []string, error) {
	proxies := []map[string]interface{}{}
	names := []string{}
	if config.Role() != "manager" {
		host := hostOnlyAddress(fallbackHost)
		if host == "" {
			return nil, nil, errors.New("无法确定本机地址")
		}
		name := strings.TrimSpace(in.Remark)
		if name == "" {
			name = "XUI"
		}
		p, err := clashProxy(in, host, name)
		if err != nil {
			return nil, nil, err
		}
		return []map[string]interface{}{p}, []string{name}, nil
	}
	nodes, err := new(ServerManagementService).Nodes()
	if err != nil {
		return nil, nil, err
	}
	used := map[string]bool{subGroupProxy: true, subGroupAuto: true, subGroupDirect: true, subGroupBlock: true, subGroupFinal: true, "DIRECT": true, "REJECT": true, "GLOBAL": true}
	for _, node := range nodes {
		if !node.Enabled || !in.TargetsNode(node.Id) {
			continue
		}
		desired, err := InboundForNode(in, node.Id)
		if err != nil {
			return nil, nil, err
		}
		// 节点没填地址时生成出来也连不上，Mihomo 会接受但整份订阅形同虚设
		address := hostOnlyAddress(node.Address)
		if address == "" {
			continue
		}
		name := strings.TrimSpace(node.Name)
		if name == "" {
			name = fmt.Sprintf("节点 %d", node.Id)
		}
		if used[name] {
			name = fmt.Sprintf("%s · %d", name, node.Id)
		}
		used[name] = true
		p, err := clashProxy(desired, address, name)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", node.Name, err)
		}
		proxies = append(proxies, p)
		names = append(names, name)
	}
	return proxies, names, nil
}

// Sign 生成订阅令牌：<入站ID>-<HMAC>，客户端无需登录即可取订阅，密钥轮换即失效。
func (s *SubscriptionService) Sign(inboundID int) (string, error) {
	mac, err := subTokenMAC(inboundID)
	if err != nil {
		return "", err
	}
	return strconv.Itoa(inboundID) + "-" + mac, nil
}

// Verify 校验订阅令牌并返回入站编号。
func (s *SubscriptionService) Verify(token string) (int, error) {
	sep := strings.Index(token, "-")
	if sep <= 0 {
		return 0, errors.New("订阅令牌无效")
	}
	id, err := strconv.Atoi(token[:sep])
	if err != nil {
		return 0, errors.New("订阅令牌无效")
	}
	mac, err := subTokenMAC(id)
	if err != nil {
		return 0, err
	}
	if !hmac.Equal([]byte(mac), []byte(token[sep+1:])) {
		return 0, errors.New("订阅令牌无效")
	}
	return id, nil
}

func subTokenMAC(inboundID int) (string, error) {
	secret, err := new(SettingService).GetSecret()
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, secret)
	fmt.Fprintf(mac, "subscription:%d", inboundID)
	return hex.EncodeToString(mac.Sum(nil))[:24], nil
}
