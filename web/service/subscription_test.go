package service

import (
	"strings"
	"testing"

	"gopkg.in/yaml.v2"
	"xui/database/model"
)

// 订阅规则文件的最小样本，覆盖普通规则、带 no-resolve 的 IP 规则和兜底规则。
const testSubscriptionRules = `[custom]
ruleset=🎯 全球直连,[]DOMAIN-SUFFIX,lan
ruleset=🎯 全球直连,[]IP-CIDR,192.168.0.0/16,no-resolve
ruleset=🛑 全球拦截,[]DOMAIN-SUFFIX,ads.example.com
ruleset=🐟 漏网之鱼,[]FINAL
`

func TestSubscriptionClashConfig(t *testing.T) {
	t.Setenv("XUI_ROLE", "agent")
	SetSubscriptionRules([]byte(testSubscriptionRules))

	in := &model.Inbound{
		Id:             7,
		Port:           8443,
		Protocol:       model.Shadowsocks,
		Remark:         "测试节点",
		Settings:       `{"method":"aes-128-gcm","password":"secret"}`,
		StreamSettings: `{}`,
		Sniffing:       `{}`,
	}
	data, err := new(SubscriptionService).ClashConfig(in, "panel.example.com:8201")
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Proxies []map[string]interface{} `yaml:"proxies"`
		Groups  []map[string]interface{} `yaml:"proxy-groups"`
		Rules   []string                 `yaml:"rules"`
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("生成的 YAML 无法解析: %v\n%s", err, data)
	}
	if len(cfg.Proxies) != 1 {
		t.Fatalf("节点数量不符: %d", len(cfg.Proxies))
	}
	// 被控端场景下节点地址取请求 Host，端口仍是入站端口
	if got := cfg.Proxies[0]["server"]; got != "panel.example.com" {
		t.Fatalf("节点地址应为请求主机名，得到 %v", got)
	}
	if got := cfg.Proxies[0]["port"]; got != 8443 {
		t.Fatalf("节点端口不符: %v", got)
	}
	names := []string{}
	for _, g := range cfg.Groups {
		names = append(names, g["name"].(string))
	}
	want := []string{subGroupProxy, subGroupAuto, subGroupDirect, subGroupBlock, subGroupFinal}
	if strings.Join(names, "|") != strings.Join(want, "|") {
		t.Fatalf("分组不符: %v", names)
	}
	joined := strings.Join(cfg.Rules, "\n")
	// no-resolve 必须排在策略组之后，顺序错了 Clash 会当成策略
	if !strings.Contains(joined, "IP-CIDR,192.168.0.0/16,🎯 全球直连,no-resolve") {
		t.Fatalf("no-resolve 规则位置不对:\n%s", joined)
	}
	if !strings.HasSuffix(joined, "MATCH,🐟 漏网之鱼") {
		t.Fatalf("缺少兜底规则:\n%s", joined)
	}
	if !strings.Contains(joined, "DOMAIN-SUFFIX,ads.example.com,🛑 全球拦截") {
		t.Fatalf("缺少广告拦截规则:\n%s", joined)
	}
}

func TestSubscriptionToken(t *testing.T) {
	clientTestDB(t)
	sub := new(SubscriptionService)
	token, err := sub.Sign(42)
	if err != nil {
		t.Fatal(err)
	}
	id, err := sub.Verify(token)
	if err != nil || id != 42 {
		t.Fatalf("令牌校验失败: id=%d err=%v", id, err)
	}
	if _, err := sub.Verify("42-000000000000"); err == nil {
		t.Fatal("错误签名被接受")
	}
	if _, err := sub.Verify("abc"); err == nil {
		t.Fatal("格式错误的令牌被接受")
	}
	if _, err := sub.Verify("99-" + token[strings.Index(token, "-")+1:]); err == nil {
		t.Fatal("改掉入站编号后签名仍然通过")
	}
}
