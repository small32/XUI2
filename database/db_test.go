package database

import (
	"path/filepath"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"x-ui/database/model"
)

// 老库升级：monthly_reset 是后加的列，升级时必须回填为“按月计算”，
// 否则老客户会静默丢掉月度清零，流量一路累计到上限后永久停用。
func TestInitInboundBackfillsMonthlyReset(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "x-ui.db")

	// 造一个升级前的老库（表里没有 monthly_reset 列）。
	legacy, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err = legacy.Exec(`CREATE TABLE inbounds (
		id integer PRIMARY KEY AUTOINCREMENT,
		user_id integer,
		up integer,
		down integer,
		total integer,
		remark text,
		enable numeric,
		expiry_time integer,
		listen text,
		port integer UNIQUE,
		protocol text,
		settings text,
		stream_settings text,
		tag text UNIQUE,
		sniffing text)`).Error; err != nil {
		t.Fatal(err)
	}
	if err = legacy.Exec(`INSERT INTO inbounds (user_id, up, down, total, remark, enable, expiry_time, port, protocol, tag) VALUES
		(1, 512, 1024, 1048576, '老客户', 1, 0, 8080, 'vless', 'inbound-8080'),
		(1, 0, 0, 0, '老客户无限制', 1, 0, 8081, 'vless', 'inbound-8081')`).Error; err != nil {
		t.Fatal(err)
	}
	if sqlDB, e := legacy.DB(); e == nil {
		defer sqlDB.Close()
	}

	// 走真实初始化流程。
	if err = InitDB(dbPath); err != nil {
		t.Fatal(err)
	}
	var rows []model.Inbound
	if err = db.Order("port").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("老数据丢失: %+v", rows)
	}
	for _, in := range rows {
		if !in.MonthlyReset {
			t.Errorf("端口 %d 未回填为按月计算: %+v", in.Port, in)
		}
	}
	// 迁移只能加列，既有数据不能被动过。
	if rows[0].Up != 512 || rows[0].Down != 1024 || rows[0].Remark != "老客户" || !rows[0].Enable {
		t.Errorf("迁移改动了既有数据: %+v", rows[0])
	}

	// 重复初始化不能把管理员手动取消的勾选又刷回 true。
	if err = db.Model(&model.Inbound{}).Where("port = ?", 8080).Update("monthly_reset", false).Error; err != nil {
		t.Fatal(err)
	}
	if err = InitDB(dbPath); err != nil {
		t.Fatal(err)
	}
	var again model.Inbound
	if err = db.Where("port = ?", 8080).First(&again).Error; err != nil {
		t.Fatal(err)
	}
	if again.MonthlyReset {
		t.Error("重复初始化把已取消的按月计费又打开了")
	}
	// 同一轮里另一个账号的勾选不受影响。
	var other model.Inbound
	if err = db.Where("port = ?", 8081).First(&other).Error; err != nil {
		t.Fatal(err)
	}
	if !other.MonthlyReset {
		t.Errorf("端口 8081 的按月标记被误改: %+v", other)
	}
}

// 全新安装：表还不存在时不该触发回填，新建的入站默认是累计计费（不按月清零）。
func TestInitInboundFreshInstallDefaultsToCumulative(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "x-ui.db")
	if err := InitDB(dbPath); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.Inbound{Port: 9090, Total: 1024, Tag: "inbound-9090"}).Error; err != nil {
		t.Fatal(err)
	}
	var in model.Inbound
	if err := db.Where("port = ?", 9090).First(&in).Error; err != nil {
		t.Fatal(err)
	}
	if in.MonthlyReset {
		t.Errorf("新建入站默认不应按月计费（勾选后才按月清零）: %+v", in)
	}
}
