package service

import (
	"encoding/json"
	"gopkg.in/yaml.v2"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"xui/database"
	"xui/database/model"
)

func clientTestDB(t *testing.T) {
	t.Helper()
	t.Setenv("XUI_ROLE", "manager")
	if err := database.InitDB(filepath.Join(t.TempDir(), "client.db")); err != nil {
		t.Fatal(err)
	}
	db, _ := database.GetDB().DB()
	t.Cleanup(func() { db.Close() })
}
func clientFixture(t *testing.T) *model.Inbound {
	t.Helper()
	db := database.GetDB()
	nodes := []model.ManagedNode{{Name: "A", Address: "a.example.com", URL: "https://offline.invalid", Enabled: true, TrafficMultiplier: multiplier(2)}, {Name: "B", Address: "b.example.com", Enabled: true, TrafficMultiplier: multiplier(0.1)}}
	for i := range nodes {
		if err := db.Create(&nodes[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	configs, _ := json.Marshal(map[string]NodeInboundConfig{"2": {Protocol: model.Trojan, Settings: `{"clients":[{"password":"other"}]}`, StreamSettings: `{"network":"ws","security":"tls","tlsSettings":{"serverName":"b.example.com"},"wsSettings":{"path":"/test"}}`, Sniffing: `{}`}})
	in := &model.Inbound{Port: 18888, Remark: "test", Enable: true, Protocol: model.Shadowsocks, Settings: `{"method":"aes-128-gcm","password":"secret"}`, SharedPassword: "secret", StreamSettings: `{}`, Sniffing: `{}`, Tag: "inbound-18888", Total: 1000, ExpiryTime: time.Now().Add(20 * 24 * time.Hour).UnixMilli(), NodeConfigs: string(configs)}
	if err := db.Create(in).Error; err != nil {
		t.Fatal(err)
	}
	for _, n := range nodes {
		r := model.NodeTraffic{NodeID: n.Id, AccountID: in.Id, Port: in.Port, Up: 100, Down: 50, Enabled: true, ObservedAt: time.Now().Unix()}
		if err := db.Create(&r).Error; err != nil {
			t.Fatal(err)
		}
	}
	return in
}
func TestClientTokensRotateAndInvalidate(t *testing.T) {
	clientTestDB(t)
	in := clientFixture(t)
	s := new(ClientService)
	if _, err := s.Login("18888", "wrong"); err == nil {
		t.Fatal("wrong password accepted")
	}
	tokens, err := s.Login("18888", "secret")
	if err != nil {
		t.Fatal(err)
	}
	row, account, err := s.Authenticate(tokens.AccessToken, false)
	if err != nil || account.Id != in.Id {
		t.Fatal(err)
	}
	if row.AccessHash == tokens.AccessToken || row.RefreshHash == tokens.RefreshToken || row.CredentialHash == "secret" {
		t.Fatal("plaintext credential stored")
	}
	next, err := s.Refresh(tokens.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Authenticate(tokens.AccessToken, false); err == nil {
		t.Fatal("old access token accepted")
	}
	if _, err := s.Refresh(tokens.RefreshToken); err == nil {
		t.Fatal("refresh replay accepted")
	}
	if _, _, err := s.Authenticate(next.AccessToken, false); err != nil {
		t.Fatal(err)
	}
	current, _, err := s.Authenticate(next.AccessToken, false)
	if err != nil {
		t.Fatal(err)
	}
	database.GetDB().Model(current).Update("access_expires", time.Now().Add(-time.Hour).Unix())
	if _, _, err := s.Authenticate(next.AccessToken, false); err == nil {
		t.Fatal("expired access token accepted")
	}
	next, err = s.Refresh(next.RefreshToken)
	if err != nil {
		t.Fatal("valid refresh must renew expired access token:", err)
	}
	if err := s.SaveSettings(&ClientSettings{Enabled: false}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Authenticate(next.AccessToken, false); err == nil {
		t.Fatal("disabled client API accepted token")
	}
	s.SaveSettings(&ClientSettings{Enabled: true})
	database.GetDB().Model(in).Update("shared_password", "new-secret")
	if _, _, err := s.Authenticate(next.AccessToken, false); err == nil {
		t.Fatal("changed password retained token")
	}
}
func TestClientAccountUsesWeightedBillingAndOfflineConfig(t *testing.T) {
	clientTestDB(t)
	in := clientFixture(t)
	s := new(ClientService)
	a, err := s.Account(in, "https://manager.example/panel/")
	if err != nil {
		t.Fatal(err)
	}
	if a.UsedBytes != 315 || a.ActualUsedBytes != 300 || *a.RemainingBytes != 685 || a.Unlimited || !a.CanProxy || a.Stale || len(a.ConfigVersion) != 64 {
		t.Fatalf("account: %+v", a)
	}
	data, err := s.ClashConfig(in, "https://manager.example/panel/")
	if err != nil {
		t.Fatal(err)
	}
	if a.ConfigVersion != ClientConfigHash(data) {
		t.Fatal("configuration version must match subscription")
	}
	var c struct {
		Mode string `yaml:"mode"`
		IPv6 bool   `yaml:"ipv6"`
		Tun  struct {
			Enable bool   `yaml:"enable"`
			Stack  string `yaml:"stack"`
		} `yaml:"tun"`
		Proxies []map[string]interface{} `yaml:"proxies"`
		Rules   []string                 `yaml:"rules"`
	}
	if err := yaml.Unmarshal(data, &c); err != nil {
		t.Fatal(err)
	}
	if c.Mode != "rule" || c.IPv6 || c.Tun.Enable || c.Tun.Stack != "mixed" || len(c.Proxies) != 2 || c.Proxies[1]["type"] != "trojan" || c.Proxies[1]["password"] != "secret" || c.Rules[0] != "DOMAIN,manager.example,DIRECT" {
		t.Fatalf("config: %s", data)
	}
	in.Total = 300
	a, err = s.Account(in, "")
	if err != nil || a.Status != "overlimit" || a.CanProxy || *a.RemainingBytes != 0 {
		t.Fatalf("quota: %+v %v", a, err)
	}
	in.Total = 0
	in.ExpiryTime = 0
	a, err = s.Account(in, "")
	if err != nil || !a.Unlimited || a.RemainingBytes != nil || a.ExpiresAt != nil {
		t.Fatalf("unlimited: %+v %v", a, err)
	}
	in.ExpiryTime = time.Now().Add(-time.Hour).UnixMilli()
	a, _ = s.Account(in, "")
	if a.CanProxy || a.Status != "expired" {
		t.Fatal("expired account allowed")
	}
	in.ExpiryTime = 0
	in.Enable = false
	a, _ = s.Account(in, "")
	if a.CanProxy || a.Status != "disabled" {
		t.Fatal("disabled account allowed")
	}
}
func TestClientPackagesAndSingleDownload(t *testing.T) {
	clientTestDB(t)
	dir := t.TempDir()
	s := new(ClientService)
	p := &model.ClientPackage{Platform: "windows", Arch: "x64", Version: "1.0", Filename: "../client.exe", Published: true}
	if err := s.StorePackage(p, strings.NewReader("installer"), dir); err != nil {
		t.Fatal(err)
	}
	if p.Filename != "client.exe" || p.Size != 9 || len(p.SHA256) != 64 {
		t.Fatalf("package: %+v", p)
	}
	rows, err := s.Packages(true)
	if err != nil || len(rows) != 1 {
		t.Fatal(err)
	}
	release, ok := AcquireClientDownload("test-account")
	if !ok {
		t.Fatal("first download denied")
	}
	if _, ok := AcquireClientDownload("test-account"); ok {
		t.Fatal("parallel download allowed")
	}
	other, ok := AcquireClientDownload("other-account")
	if !ok {
		t.Fatal("unrelated account blocked")
	}
	other()
	release()
	release, ok = AcquireClientDownload("test-account")
	if !ok {
		t.Fatal("slot not released")
	}
	release()
	if err := s.DeletePackage(p.Id, dir); err != nil {
		t.Fatal(err)
	}
	rows, _ = s.Packages(true)
	if len(rows) != 0 {
		t.Fatal("deleted package still published")
	}
	for _, u := range []string{"javascript:alert(1)", "https://user:password@example.com/", "https://example.com/?password=x"} {
		if _, err := ValidateClientURL(u); err == nil {
			t.Fatalf("invalid URL accepted: %s", u)
		}
	}
	if u, err := ValidateClientURL("https://example.com/panel"); err != nil || u != "https://example.com/panel/" {
		t.Fatalf("path prefix: %s %v", u, err)
	}
}

func TestClientTablesAreNotAddedToAgent(t *testing.T) {
	t.Setenv("XUI_ROLE", "agent")
	if err := database.InitDB(filepath.Join(t.TempDir(), "agent.db")); err != nil {
		t.Fatal(err)
	}
	db := database.GetDB()
	sqlDB, _ := db.DB()
	t.Cleanup(func() { sqlDB.Close() })
	if db.Migrator().HasTable(&model.ClientSession{}) || db.Migrator().HasTable(&model.ClientPackage{}) {
		t.Fatal("agent received manager-only client tables")
	}
}

func TestClientClashReportsUnsupportedConfiguration(t *testing.T) {
	clientTestDB(t)
	in := clientFixture(t)
	s := new(ClientService)
	in.StreamSettings = `{"network":"kcp"}`
	account, err := s.Account(in, "")
	if err != nil || account.CanProxy || account.Status != "invalid_config" || !strings.Contains(account.ConfigError, "kcp") {
		t.Fatalf("unsupported config: %+v %v", account, err)
	}
	in.StreamSettings = `{}`
	if _, err := clashProxy(in, "2001:db8::1", "v6"); err == nil {
		t.Fatal("IPv6 node accepted")
	}
}
