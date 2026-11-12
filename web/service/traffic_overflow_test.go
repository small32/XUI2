package service

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
	"xui/database"
	"xui/database/model"
	"xui/util/common"
)

func TestTrafficSaturatingArithmetic(t *testing.T) {
	for _, tc := range []struct {
		values []int64
		want   int64
	}{
		{[]int64{100, 50, 200}, 350},
		{[]int64{math.MaxInt64, 1}, math.MaxInt64},
		{[]int64{math.MaxInt64, math.MaxInt64}, math.MaxInt64},
		{[]int64{-100, 50}, -50},
		{[]int64{math.MinInt64, -1}, math.MinInt64},
		{[]int64{math.MaxInt64, math.MinInt64}, -1},
	} {
		if got := sumTraffic(tc.values...); got != tc.want {
			t.Fatalf("sum %v = %d, want %d", tc.values, got, tc.want)
		}
	}
	for _, tc := range []struct{ limit, used, want int64 }{
		{1000, 350, 650},
		{1000, 1000, 0},
		{1000, math.MaxInt64, 0},
		{math.MaxInt64, math.MaxInt64, 0},
		{math.MaxInt64, math.MinInt64, math.MaxInt64},
	} {
		if got := remainingTraffic(tc.limit, tc.used); got != tc.want {
			t.Fatalf("remaining (%d, %d) = %d, want %d", tc.limit, tc.used, got, tc.want)
		}
	}
	if !TrafficOverlimit(math.MaxInt64, 1, math.MaxInt64) || TrafficOverlimit(math.MaxInt64, 1, 0) {
		t.Fatal("overflow changed the limited/unlimited quota decision")
	}
	value := 1e19
	if err := validateTrafficMultiplier(&value); err != nil {
		t.Fatal("large finite multipliers must remain valid:", err)
	}
	if got := scaledNodeTraffic(1, model.ManagedNode{TrafficMultiplier: &value}); got != math.MaxInt64 {
		t.Fatalf("large weighted counter = %d", got)
	}
	if got := scaledNodeTraffic(-10, model.ManagedNode{TrafficMultiplier: multiplier(2)}); got != -20 {
		t.Fatalf("negative legacy counter was changed: %d", got)
	}
}

func TestTrafficMultiplierSaturatesSharedUsage(t *testing.T) {
	clientTestDB(t)
	in := clientFixture(t)
	db := database.GetDB()
	agent := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"applied":true}`))
	}))
	defer agent.Close()
	hash := sha256.Sum256(agent.Certificate().Raw)
	if err := db.Model(&model.ManagedNode{}).Where("1 = 1").Updates(map[string]interface{}{
		"traffic_multiplier": 1e19, "url": agent.URL, "token": strings.Repeat("a", 64), "cert_sha256": hex.EncodeToString(hash[:]),
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&model.NodeTraffic{}).Where("1 = 1").Updates(map[string]interface{}{"up": 1, "down": 1}).Error; err != nil {
		t.Fatal(err)
	}
	s := new(ServerManagementService)
	cache, err := s.GetTrafficCache()
	if err != nil || len(cache) != 1 {
		t.Fatalf("cache: %+v %v", cache, err)
	}
	if cache[0].Up != math.MaxInt64 || cache[0].Down != math.MaxInt64 || cache[0].Used != math.MaxInt64 {
		t.Fatalf("weighted shared counters wrapped: %+v", cache[0])
	}
	summary, err := s.Summary(in.Id)
	if err != nil || len(summary) != 1 {
		t.Fatalf("summary: %+v %v", summary, err)
	}
	if summary[0].Remote != math.MaxInt64 || summary[0].Total != math.MaxInt64 || !summary[0].Overlimit {
		t.Fatalf("weighted quota wrapped: %+v", summary[0])
	}
	usage, err := s.NodeUsage(in.Port)
	if err != nil || len(usage) != 2 {
		t.Fatalf("node usage: %+v %v", usage, err)
	}
	for _, row := range usage {
		if row.Used != math.MaxInt64 || row.ActualUsed != 2 || row.Up != 1 || row.Down != 1 {
			t.Fatalf("node usage wrapped or raw counters changed: %+v", row)
		}
	}
	nodes, err := s.ListNodes()
	if err != nil || len(nodes) != 2 {
		t.Fatalf("node list: %+v %v", nodes, err)
	}
	for _, row := range nodes {
		if row.Used != math.MaxInt64 {
			t.Fatalf("node total wrapped: %+v", row)
		}
	}
	account, err := new(ClientService).Account(in, "https://manager.example/panel/")
	if err != nil {
		t.Fatal(err)
	}
	if account.UsedBytes != math.MaxInt64 || account.ActualUsedBytes != 4 || account.RemainingBytes == nil || *account.RemainingBytes != 0 || account.Status != "overlimit" || account.CanProxy {
		t.Fatalf("client billing wrapped: %+v", account)
	}
	local := model.NodeTraffic{NodeID: nodes[0].Id, AccountID: -22, Port: in.Port + 1, Up: 1, Down: 1, Total: 1, Enabled: true, ObservedAt: time.Now().Unix()}
	if err := db.Create(&local).Error; err != nil {
		t.Fatal(err)
	}
	all, err := s.Summary(0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range all {
		if row.LocalInboundID == 22 {
			found = true
			if row.Total != math.MaxInt64 || row.ActualUsed != 2 || !row.Overlimit {
				t.Fatalf("node local quota wrapped: %+v", row)
			}
		}
	}
	if !found {
		t.Fatal("missing node local usage")
	}
	if err := s.enforceLimits(); err != nil {
		t.Fatal(err)
	}
	if err := db.First(in, in.Id).Error; err != nil || in.Enable || in.DisabledBy != "limit" {
		t.Fatalf("weighted quota was not enforced: %+v %v", in, err)
	}
	var raw []model.NodeTraffic
	if err := db.Find(&raw).Error; err != nil {
		t.Fatal(err)
	}
	for _, reading := range raw {
		if reading.Up != 1 || reading.Down != 1 {
			t.Fatalf("raw reading changed: %+v", reading)
		}
	}
	// Local and actual usage must obey the same limit when raw counters alone
	// are representable individually but their total is not.
	in.Up, in.Down = math.MaxInt64, 1
	if err := db.Model(in).Updates(map[string]interface{}{"up": in.Up, "down": in.Down}).Error; err != nil {
		t.Fatal(err)
	}
	account, err = new(ClientService).Account(in, "")
	if err != nil || account.ActualUsedBytes != math.MaxInt64 || account.UsedBytes != math.MaxInt64 || *account.RemainingBytes != 0 {
		t.Fatalf("local/actual total wrapped: %+v %v", account, err)
	}
}

func TestTrafficMultiplierMonthlySaturatesTotals(t *testing.T) {
	for _, late := range []bool{false, true} {
		name := "new archive"
		if late {
			name = "late snapshot"
		}
		t.Run(name, func(t *testing.T) {
			clientTestDB(t)
			db := database.GetDB()
			agent := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if strings.HasSuffix(r.URL.Path, "/traffic/reset") {
					w.Write([]byte(`{"up":1,"down":1}`))
					return
				}
				w.Write([]byte(`{"applied":true}`))
			}))
			defer agent.Close()
			hash := sha256.Sum256(agent.Certificate().Raw)
			nodes := []model.ManagedNode{
				{Name: "first", Enabled: true, URL: agent.URL, Token: strings.Repeat("a", 64), CertSHA256: hex.EncodeToString(hash[:]), TrafficMultiplier: multiplier(1e19)},
				{Name: "second", Enabled: true, URL: agent.URL, Token: strings.Repeat("b", 64), CertSHA256: hex.EncodeToString(hash[:]), TrafficMultiplier: multiplier(1e19)},
			}
			for i := range nodes {
				if err := db.Create(&nodes[i]).Error; err != nil {
					t.Fatal(err)
				}
			}
			in := model.Inbound{Port: 32109, Tag: "inbound-32109", Enable: true, Up: math.MaxInt64, Down: 1, Total: 1000, Protocol: model.Shadowsocks, SharedPassword: "secret", Settings: `{"method":"aes-128-gcm","password":"secret"}`, StreamSettings: `{}`, Sniffing: `{}`, MonthlyReset: true}
			if err := db.Create(&in).Error; err != nil {
				t.Fatal(err)
			}
			period := previousMonth(time.Now().In(common.ShanghaiLocation))
			if late {
				archive := model.TrafficSnapshot{InboundId: in.Id, Yyyymm: period, Port: in.Port, LocalUp: in.Up, LocalDown: in.Down, Total: in.Total}
				if err := db.Create(&archive).Error; err != nil {
					t.Fatal(err)
				}
			}
			if err := setValue(db, managerMonthKey, strconv.Itoa(period)); err != nil {
				t.Fatal(err)
			}
			s := new(ServerManagementService)
			if err := s.MaybeMonthlyReset(); err != nil {
				t.Fatal(err)
			}
			var archive model.TrafficSnapshot
			if err := db.Where("inbound_id = ? AND yyyymm = ?", in.Id, period).First(&archive).Error; err != nil {
				t.Fatal(err)
			}
			if archive.RemoteUp != math.MaxInt64 || archive.RemoteDown != math.MaxInt64 || archive.LocalUp != math.MaxInt64 || archive.LocalDown != 1 {
				t.Fatalf("monthly archive wrapped: %+v", archive)
			}
			var pending int64
			if err := db.Model(&model.SyncTask{}).Where("account_id = ? AND kind = ? AND status <> ?", in.Id, "reset", "done").Count(&pending).Error; err != nil || pending != 0 {
				t.Fatalf("large snapshot failed to settle: %d %v", pending, err)
			}
			history, err := s.TrafficResetSnapshots(in.Id)
			if err != nil || len(history.Rows) != 1 {
				t.Fatalf("history: %+v %v", history, err)
			}
			row := history.Rows[0]
			if row.Local != math.MaxInt64 || row.Remote != math.MaxInt64 || row.Used != math.MaxInt64 || len(row.NodeUsed) != 2 {
				t.Fatalf("historical totals wrapped: %+v", row)
			}
			for name, used := range row.NodeUsed {
				if used != math.MaxInt64 {
					t.Fatalf("historical node %q wrapped: %d", name, used)
				}
			}
		})
	}
}
