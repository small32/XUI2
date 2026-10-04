package service

import (
	"path/filepath"
	"strconv"
	"testing"
	"xui/database"
	"xui/database/model"
)

func TestManagedInboundOverwritesSamePortAndPreservesTraffic(t *testing.T) {
	for _, accountID := range []int{0, 99, 42} {
		t.Run(strconv.Itoa(accountID), func(t *testing.T) {
			if err := database.InitDB(filepath.Join(t.TempDir(), "overwrite.db")); err != nil {
				t.Fatal(err)
			}
			db := database.GetDB()
			sqlDB, err := db.DB()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { sqlDB.Close() })
			old := model.Inbound{ManagerAccountID: accountID, Port: 32101, Tag: "inbound-32101", Protocol: model.Trojan, Settings: `{}`, StreamSettings: `{}`, Up: 123, Down: 456, Total: 100, Remark: "old", Enable: false, DisabledBy: "limit"}
			if err := db.Create(&old).Error; err != nil {
				t.Fatal(err)
			}
			in := model.Inbound{Id: 999, ManagerAccountID: 42, ManagerRevision: "new", Port: 32101, Protocol: model.VLESS, Settings: `{"clients":[]}`, StreamSettings: `{}`, Sniffing: `{}`, Up: 9999, Down: 9999, Remark: "new", Enable: true, Total: 2000}
			for i := 0; i < 2; i++ {
				if err := UpsertManagedInbound(in); err != nil {
					t.Fatal(err)
				}
			}
			var stored model.Inbound
			if err := db.First(&stored, old.Id).Error; err != nil {
				t.Fatal(err)
			}
			if stored.ManagerAccountID != 42 || stored.ManagerRevision != "new" || stored.Protocol != model.VLESS || stored.Settings != in.Settings || stored.Remark != "new" || stored.Total != 2000 || !stored.Enable || stored.DisabledBy != "" || stored.Up != 123 || stored.Down != 456 {
				t.Fatalf("manager config or counters wrong: %+v", stored)
			}
			var count int64
			db.Model(&model.Inbound{}).Count(&count)
			if count != 1 {
				t.Fatal("overwrite created a duplicate")
			}
		})
	}
}

func TestManagedInboundPortMoveOverwritesOccupiedPort(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "move.db")); err != nil {
		t.Fatal(err)
	}
	db := database.GetDB()
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	old := model.Inbound{ManagerAccountID: 42, Port: 32101, Tag: "inbound-32101", Up: 10, Down: 20}
	occupied := model.Inbound{Port: 32102, Tag: "inbound-32102", Up: 30, Down: 40}
	for _, row := range []*model.Inbound{&old, &occupied} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	in := model.Inbound{ManagerAccountID: 42, Port: 32102, Protocol: model.VLESS, Settings: `{}`, StreamSettings: `{}`, Enable: true}
	for i := 0; i < 2; i++ {
		if err := UpsertManagedInbound(in); err != nil {
			t.Fatal(err)
		}
	}
	var stored model.Inbound
	if err := db.First(&stored, old.Id).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Port != 32102 || stored.Up != 40 || stored.Down != 60 || stored.Tag != "inbound-32102" {
		t.Fatalf("move lost config or traffic: %+v", stored)
	}
	var count int64
	db.Model(&model.Inbound{}).Count(&count)
	if count != 1 {
		t.Fatal("occupied inbound remains")
	}
}
