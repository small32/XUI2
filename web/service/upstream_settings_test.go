package service

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"xui/database"
	"xui/database/model"
	"xui/xray"
)

const manualProxyTemplate = `{"api":{"tag":"api"},"inbounds":[],"outbounds":[{"tag":"to-b","protocol":"trojan","settings":{"servers":[{"address":"example.com","port":443,"password":"secret"}]}}],"routing":{"rules":[{"type":"field","network":"tcp,udp","outboundTag":"to-b"}]}}`

func TestLocalProxySettingsPreserveManualTemplateAndRejectInboundPortConflict(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "upstream.db")); err != nil {
		t.Fatal(err)
	}
	sqlDB, err := database.GetDB().DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	t.Setenv("XUI_ROLE", "agent")
	s := &SettingService{}
	settings, err := s.GetAllSetting()
	if err != nil {
		t.Fatal(err)
	}
	settings.LocalProxyEnable = true
	settings.XrayTemplateConfig = manualProxyTemplate
	// Legacy form data must never overwrite the manually edited template.
	if err := s.saveSetting("upstreamEnabled", "true"); err != nil {
		t.Fatal(err)
	}
	if err := s.saveSetting("upstreamConfig", `{"protocol":"trojan","address":"obsolete.example","port":29050,"password":"obsolete"}`); err != nil {
		t.Fatal(err)
	}
	in := model.Inbound{Port: xray.LocalHTTPPort, Tag: "inbound-10809", Protocol: model.Trojan, Settings: `{"clients":[{"password":"keep"}]}`}
	if err := database.GetDB().Create(&in).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateAllSetting(settings); err == nil {
		t.Fatal("local inbound port conflict accepted")
	}
	unchanged, err := s.GetAllSetting()
	if err != nil || unchanged.LocalProxyEnable {
		t.Fatal("invalid settings partially saved")
	}
	if err := database.GetDB().Delete(&in).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateAllSetting(settings); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.GetAllSetting()
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.LocalProxyEnable || loaded.XrayTemplateConfig != manualProxyTemplate {
		t.Fatal("upstream settings did not persist")
	}
	if strings.Contains(loaded.XrayTemplateConfig, xray.LocalHTTPTag) {
		t.Fatal("saved template was modified")
	}
	loaded.LocalProxyEnable = false
	if err := s.UpdateAllSetting(loaded); err != nil {
		t.Fatal(err)
	}
	loaded, _ = s.GetAllSetting()
	if loaded.XrayTemplateConfig != manualProxyTemplate {
		t.Fatal("manual upstream changed on disable")
	}
}

func TestManagerLocalProxyNeverStartsSharedAccountInbounds(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "manager-upstream.db")); err != nil {
		t.Fatal(err)
	}
	sqlDB, err := database.GetDB().DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	t.Setenv("XUI_ROLE", "manager")
	s := &SettingService{}
	settings, err := s.GetAllSetting()
	if err != nil {
		t.Fatal(err)
	}
	settings.LocalProxyEnable = true
	settings.XrayTemplateConfig = manualProxyTemplate
	if err := s.UpdateAllSetting(settings); err != nil {
		t.Fatal(err)
	}
	in := model.Inbound{Port: 23456, Tag: "inbound-23456", Protocol: model.Trojan, Enable: true, Settings: `{"clients":[{"password":"account-password"}]}`}
	if err := database.GetDB().Create(&in).Error; err != nil {
		t.Fatal(err)
	}
	cfg, err := (&XrayService{}).GetXrayConfig()
	if err != nil {
		t.Fatal(err)
	}
	for _, in := range cfg.InboundConfigs {
		if in.Port == 23456 {
			t.Fatal("manager started a managed account inbound")
		}
	}
	data, _ := json.Marshal(cfg)
	if !strings.Contains(string(data), xray.LocalHTTPTag) {
		t.Fatal("manager local proxy missing")
	}
}

func TestSystemProxyTrafficDoesNotConsumeInboundAccountQuota(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "proxy-traffic.db")); err != nil {
		t.Fatal(err)
	}
	sqlDB, err := database.GetDB().DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	in := model.Inbound{Port: 23456, Tag: "inbound-23456", Up: 100, Down: 200, Total: 1000}
	if err := database.GetDB().Create(&in).Error; err != nil {
		t.Fatal(err)
	}
	traffic := []*xray.Traffic{
		{IsInbound: true, Tag: xray.LocalHTTPTag, Up: 10000, Down: 20000},
		{IsInbound: true, Tag: xray.LocalSOCKSTag, Up: 10000, Down: 20000},
		{IsInbound: true, Tag: in.Tag, Up: 3, Down: 4},
	}
	if err := (&InboundService{}).AddTraffic(traffic); err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().First(&in, in.Id).Error; err != nil {
		t.Fatal(err)
	}
	if in.Up != 103 || in.Down != 204 || in.Total != 1000 {
		t.Fatalf("proxy traffic mixed into account: %+v", in)
	}
}
