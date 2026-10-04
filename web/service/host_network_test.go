package service

import (
	"path/filepath"
	"testing"
	"time"
	"xui/database"
	"xui/util/common"
)

func TestHostNetworkMonthlyTrafficPersistsAndHandlesRebootAndMonthChange(t *testing.T) {
	path := filepath.Join(t.TempDir(), "host-network.db")
	if err := database.InitDB(path); err != nil {
		t.Fatal(err)
	}
	sqlDB, err := database.GetDB().DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, common.ShanghaiLocation)
	boot := uint64(now.Add(-time.Hour).Unix())
	sample := func(at time.Time, b uint64, up, down uint64) {
		t.Helper()
		if err := recordHostNetwork(at, b, map[string]NetworkCounter{"eth0": {up, down}}); err != nil {
			t.Fatal(err)
		}
	}
	sample(now, boot, 1000, 2000)
	sample(now.Add(10*time.Second), boot, 1500, 2800)
	got, err := readHostMonthTraffic(now)
	if err != nil || got.Sent != 500 || got.Recv != 800 || !got.Ready {
		t.Fatalf("initial counters included or delta missing: %+v %v", got, err)
	}
	// Reopening the DB models a panel restart: persisted baselines avoid counting historical bytes again.
	sqlDB.Close()
	if err := database.InitDB(path); err != nil {
		t.Fatal(err)
	}
	sqlDB, err = database.GetDB().DB()
	if err != nil {
		t.Fatal(err)
	}
	sample(now.Add(20*time.Second), boot, 1600, 2900)
	got, _ = readHostMonthTraffic(now)
	if got.Sent != 600 || got.Recv != 900 {
		t.Fatalf("restart lost or duplicated usage: %+v", got)
	}
	sample(now.Add(30*time.Second), uint64(now.Add(25*time.Second).Unix()), 50, 60)
	got, _ = readHostMonthTraffic(now)
	if got.Sent != 650 || got.Recv != 960 {
		t.Fatalf("reboot reset monthly traffic: %+v", got)
	}
	next := time.Date(2026, 11, 1, 0, 0, 1, 0, common.ShanghaiLocation)
	sample(next, boot, 5000, 6000)
	sample(next.Add(10*time.Second), boot, 5020, 6030)
	got, _ = readHostMonthTraffic(next)
	if got.Month != "2026-11" || got.Sent != 20 || got.Recv != 30 {
		t.Fatalf("previous month included: %+v", got)
	}
	old, _ := readHostMonthTraffic(now)
	if old.Sent != 650 || old.Recv != 960 {
		t.Fatal("previous month history overwritten")
	}
}

func TestHostNetworkDeltaHandlesInterfaceResetWithoutUnderflow(t *testing.T) {
	old := map[string]NetworkCounter{"eth0": {1000, 2000}, "removed": {500, 500}}
	current := map[string]NetworkCounter{"eth0": {50, 60}, "new": {99999, 99999}}
	up, down := networkDelta(current, old)
	if up != 50 || down != 60 {
		t.Fatalf("reset/new interface caused invalid traffic: %d %d", up, down)
	}
}
