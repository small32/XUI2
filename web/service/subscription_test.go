package service

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"gopkg.in/yaml.v2"
	"xui/database"
	"xui/database/model"
)

// 订阅规则文件的最小样本，覆盖普通规则、带 no-resolve 的 IP 规则和兜底规则。
// 最后一条是 Surge 专用的 URL-REGEX：Clash 不支持，生成时必须丢弃，否则整份配置校验失败。
const testSubscriptionRules = `[custom]
ruleset=🎯 全球直连,[]DOMAIN-SUFFIX,lan
ruleset=🎯 全球直连,[]IP-CIDR,192.168.0.0/16,no-resolve
ruleset=🛑 全球拦截,[]DOMAIN-SUFFIX,ads.example.com
ruleset=🚀 节点选择,[]URL-REGEX,^https?://video\.example\.com/
ruleset=❓ 不存在的组,[]DOMAIN-SUFFIX,orphan.example.com
ruleset=🐟 漏网之鱼,[]FINAL
`

// 规则文件可能晚于首次调用注入，失败不能被缓存，否则订阅永久 500。
func TestSubscriptionRulesRetryAfterLateInjection(t *testing.T) {
	SetSubscriptionRules(nil)
	if _, err := loadSubRules(); err == nil {
		t.Fatal("规则未注入时应当报错")
	}
	SetSubscriptionRules([]byte(testSubscriptionRules))
	if _, err := loadSubRules(); err != nil {
		t.Fatalf("注入规则后应当可用: %v", err)
	}
}

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
	// Clash 不认 URL-REGEX，混进去会导致客户端整体校验失败
	if strings.Contains(joined, "URL-REGEX") {
		t.Fatalf("不支持的规则类型没有被过滤:\n%s", joined)
	}
	// 分组名与规则里的 emoji 由上面的解析结果校验，YAML 转义方式不影响语义。
	// 规则指向不存在的策略组时，Clash 会拒绝整份配置，这类规则必须丢弃
	if strings.Contains(joined, "orphan.example.com") {
		t.Fatalf("指向未知策略组的规则没有被丢弃:\n%s", joined)
	}
}

func TestSubscriptionClashConfigPreservesStoredPasswords(t *testing.T) {
	clientTestDB(t)
	t.Setenv("XUI_ROLE", "agent")
	SetSubscriptionRules([]byte(testSubscriptionRules))

	passwords := []struct {
		name  string
		value string
	}{
		{"literal Unicode escape", `abc\U0001F680xyz`},
		{"double quoted Unicode escape", `abc\U0001F680xyz🚀`},
		{"double quoted escaped quote", `abc\U00000022xyz🚀`},
		{"YAML special text", "\"quotes\": 'single' # comment: [a, b] {key: value} &anchor *alias !tag | > % @ \\path\tend"},
	}
	for protocolIndex, protocol := range []model.Protocol{model.Shadowsocks, model.Trojan} {
		for passwordIndex, password := range passwords {
			t.Run(string(protocol)+"/"+password.name, func(t *testing.T) {
				settings := map[string]interface{}{"method": "aes-128-gcm", "password": password.value}
				stream := `{}`
				if protocol == model.Trojan {
					settings = map[string]interface{}{"clients": []map[string]string{{"password": password.value}}}
					stream = `{"security":"tls","tlsSettings":{"serverName":"panel.example.com"}}`
				}
				raw, err := json.Marshal(settings)
				if err != nil {
					t.Fatal(err)
				}
				in := &model.Inbound{
					Port:           8443 + protocolIndex*len(passwords) + passwordIndex,
					Tag:            t.Name(),
					Protocol:       protocol,
					Remark:         "测试节点",
					Settings:       string(raw),
					StreamSettings: stream,
					Sniffing:       `{}`,
				}
				// 直接落库模拟旧入站：新增/修改密码的格式校验不应改变存量订阅。
				if err := database.GetDB().Create(in).Error; err != nil {
					t.Fatal(err)
				}
				stored, err := new(InboundService).GetInbound(in.Id)
				if err != nil {
					t.Fatal(err)
				}
				data, err := new(SubscriptionService).ClashConfig(stored, "panel.example.com:8201")
				if err != nil {
					t.Fatal(err)
				}
				var cfg struct {
					Proxies []struct {
						Password string `yaml:"password"`
					} `yaml:"proxies"`
				}
				if err := yaml.Unmarshal(data, &cfg); err != nil {
					t.Fatalf("生成的 YAML 无法解析: %v\n%s", err, data)
				}
				if len(cfg.Proxies) != 1 {
					t.Fatalf("节点数量不符: %d", len(cfg.Proxies))
				}
				if got := cfg.Proxies[0].Password; got != password.value {
					t.Fatalf("存量密码被改写: got %q, want %q", got, password.value)
				}
			})
		}
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

// secret 的默认值是进程启动时随机生成的，落库后与默认值恒等，
// 旧实现每次调用都会 UPDATE 一次；订阅接口是公开的，必须只写一次。
func TestGetSecretWritesOnce(t *testing.T) {
	clientTestDB(t)
	svc := new(SettingService)
	first, err := svc.GetSecret()
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.GetSecret()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatalf("两次读取的 secret 不一致")
	}
	var count int64
	if err := database.GetDB().Model(&model.Setting{}).Where("key = ?", "secret").Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("secret 记录条数异常: %d", count)
	}
}
