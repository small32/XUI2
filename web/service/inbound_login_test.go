package service

import (
	"path/filepath"
	"testing"

	"xui/database"
	"xui/database/model"
)

func TestInboundLoginUsesPortAndPassword(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "login.db")); err != nil {
		t.Fatal(err)
	}
	db := database.GetDB()
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	for _, in := range []model.Inbound{
		{Port: 12345, Tag: "inbound-12345", Remark: "customer", Protocol: model.Trojan, Settings: `{"clients":[{"password":"secret"}]}`},
		{Port: 23456, Tag: "inbound-23456", Remark: "mixed", Protocol: model.VLESS, Settings: `{"clients":[{"id":"uuid"}]}`, SharedPassword: "shared-secret"},
	} {
		if err := db.Create(&in).Error; err != nil {
			t.Fatal(err)
		}
	}
	s := &InboundService{}
	for _, c := range []struct {
		username, password string
		port               int
	}{
		{"12345", "secret", 12345},
		{"23456", "shared-secret", 23456},
		{"customer", "secret", 0},
		{"mixed", "shared-secret", 0},
		{"12345", "wrong", 0},
		{"12345", "", 0},
		{"54321", "secret", 0},
		{"0", "secret", 0},
		{"65536", "secret", 0},
	} {
		got := s.CheckInboundCredential(c.username, c.password)
		if c.port == 0 {
			if got != nil {
				t.Errorf("unexpected login for %q", c.username)
			}
		} else if got == nil || got.Port != c.port {
			t.Errorf("port login failed for %q", c.username)
		}
	}
}
