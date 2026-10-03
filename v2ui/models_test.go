package v2ui

import (
	"path/filepath"
	"testing"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestLegacyInboundPreservesLimitAndExpiry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TABLE inbound (id integer PRIMARY KEY, port integer, total integer, expiry_time integer)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`INSERT INTO inbound (id, port, total, expiry_time) VALUES (1, 443, 1048576, 1893456000000)`).Error; err != nil {
		t.Fatal(err)
	}
	var legacy V2Inbound
	if err := db.First(&legacy).Error; err != nil {
		t.Fatal(err)
	}
	got := legacy.ToInbound(7)
	if got.Total != 1048576 || got.ExpiryTime != 1893456000000 {
		t.Fatalf("legacy account limits were lost: %+v", got)
	}
}
