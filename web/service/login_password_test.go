package service

import (
	"strings"
	"testing"
	"xui/database/model"
)

func TestLoginPasswordSnapshot(t *testing.T) {
	for _, tc := range []struct {
		protocol model.Protocol
		settings string
	}{
		{model.Trojan, `{"clients":[{"password":"new-secret"},{"password":"new-secret"}]}`},
		{model.Shadowsocks, `{"method":"aes-128-gcm","password":"new-secret"}`},
		{model.Socks, `{"accounts":[{"user":"user","pass":"new-secret"}]}`},
		{model.Http, `{"accounts":[{"user":"user","pass":"new-secret"}]}`},
	} {
		for _, password := range []string{"old-secret", "new-secret"} {
			out, err := WithLoginPassword(tc.protocol, tc.settings, password)
			if err != nil {
				t.Fatal(err)
			}
			if inboundPassword(&model.Inbound{Protocol: tc.protocol, Settings: out}) != password {
				t.Fatal("snapshot not applied", out)
			}
			if password == "old-secret" && strings.Contains(out, "new-secret") {
				t.Fatal("new credential leaked", out)
			}
		}
	}
	for _, protocol := range []model.Protocol{model.VLESS, model.VMess} {
		raw := `{"clients":[{"id":"node-uuid"}]}`
		out, err := WithLoginPassword(protocol, raw, "shared-password")
		if err != nil || out != raw {
			t.Fatal("UUID protocol configuration must remain unchanged", err)
		}
	}
	if _, err := WithLoginPassword(model.Dokodemo, `{}`, "old"); err == nil {
		t.Fatal("unsupported subscription protocol must fail closed")
	}
}
