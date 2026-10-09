package service

import (
	"crypto/sha256"
	"encoding/hex"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"math"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"xui/database"
	"xui/database/model"
	"xui/util/common"
)

func multiplier(value float64) *float64 { return &value }

func TestTrafficMultiplierValidationAndBytes(t *testing.T) {
	for _, value := range []float64{0, 0.1, 1, 2} {
		if err := validateTrafficMultiplier(&value); err != nil {
			t.Fatal(err)
		}
		if got := scaledNodeTraffic(1000000000, model.ManagedNode{TrafficMultiplier: &value}); got != int64(1000000000*value) {
			t.Fatalf("multiplier %v: %d", value, got)
		}
	}
	for _, value := range []float64{-1, math.NaN(), math.Inf(1)} {
		if validateTrafficMultiplier(&value) == nil {
			t.Fatalf("accepted %v", value)
		}
	}
	if scaledNodeTraffic(123, model.ManagedNode{}) != 123 {
		t.Fatal("omitted multiplier must preserve traffic")
	}
}

func TestTrafficMultiplierCacheSummaryAndPersistence(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "multiplier.db")); err != nil {
		t.Fatal(err)
	}
	db := database.GetDB()
	sqlDB, _ := db.DB()
	t.Cleanup(func() { sqlDB.Close() })
	nodes := []model.ManagedNode{
		{Name: "double", Enabled: true, TrafficMultiplier: multiplier(2)},
		{Name: "decimal", Enabled: true, TrafficMultiplier: multiplier(0.1)},
		{Name: "free", Enabled: true, TrafficMultiplier: multiplier(0)},
		{Name: "default", Enabled: true},
	}
	for i := range nodes {
		if err := db.Create(&nodes[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	var saved model.ManagedNode
	db.First(&saved, nodes[2].Id)
	if saved.TrafficMultiplier == nil || *saved.TrafficMultiplier != 0 {
		t.Fatal("zero multiplier was replaced by default")
	}
	var defaultNode model.ManagedNode
	db.First(&defaultNode, nodes[3].Id)
	if defaultNode.TrafficMultiplier == nil || *defaultNode.TrafficMultiplier != 1 {
		t.Fatal("new node multiplier does not default to one")
	}
	in := model.Inbound{Port: 32101, Tag: "inbound-32101", Enable: true, Total: 500, Protocol: model.Shadowsocks, Settings: `{"password":"test"}`, StreamSettings: `{}`, Sniffing: `{}`}
	if err := db.Create(&in).Error; err != nil {
		t.Fatal(err)
	}
	readings := []model.NodeTraffic{
		{NodeID: nodes[0].Id, AccountID: in.Id, Port: in.Port, Up: 100, Down: 50},
		{NodeID: nodes[0].Id, AccountID: -10, Port: in.Port, Up: 25, Down: 25},
		{NodeID: nodes[1].Id, AccountID: in.Id, Port: in.Port, Up: 1000, Down: 500},
		{NodeID: nodes[2].Id, AccountID: in.Id, Port: in.Port, Up: 10000, Down: 10000},
	}
	for i := range readings {
		readings[i].Enabled = true
		readings[i].ObservedAt = time.Now().Unix()
		if err := db.Create(&readings[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	s := new(ServerManagementService)
	for i := 0; i < 2; i++ {
		cache, err := s.GetTrafficCache()
		if err != nil || len(cache) != 1 || cache[0].Used != 550 {
			t.Fatalf("cache: %+v %v", cache, err)
		}
	}
	summary, err := s.Summary(in.Id)
	if err != nil || len(summary) != 1 || summary[0].Remote != 550 || !summary[0].Overlimit {
		t.Fatalf("weighted shared quota: %+v %v", summary[0], err)
	}
	usage, err := s.NodeUsage(in.Port)
	if err != nil || usage[0].Used != 400 || usage[1].Used != 150 || usage[2].Used != 0 {
		t.Fatalf("node usage: %+v %v", usage, err)
	}
	if usage[0].ActualUsed != 200 || usage[0].Up != 125 || usage[0].Down != 75 || usage[0].TrafficMultiplier != 2 || usage[1].ActualUsed != 1500 || usage[1].TrafficMultiplier != 0.1 || usage[2].ActualUsed != 20000 {
		t.Fatalf("raw detail and multiplier: %+v", usage)
	}
	listed, err := s.ListNodes()
	if err != nil || listed[0].Used != 400 || listed[1].Used != 150 {
		t.Fatalf("node list: %+v %v", listed, err)
	}
	db.Model(&nodes[0]).Update("traffic_multiplier", 1)
	cache, err := s.GetTrafficCache()
	if err != nil || cache[0].Used != 350 {
		t.Fatalf("multiplier edit should recalculate from raw counters: %+v %v", cache, err)
	}
	var raw model.NodeTraffic
	db.First(&raw, readings[0].Id)
	if raw.Up != 100 || raw.Down != 50 {
		t.Fatal("raw agent counters changed")
	}
}

func TestTrafficMultiplierLegacyMigrationAndNodeSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.db")
	legacy, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := legacy.Exec("CREATE TABLE managed_nodes (id integer PRIMARY KEY, name text)").Error; err != nil {
		t.Fatal(err)
	}
	if err := legacy.Exec("INSERT INTO managed_nodes (id,name) VALUES (1,'legacy')").Error; err != nil {
		t.Fatal(err)
	}
	oldSQL, _ := legacy.DB()
	oldSQL.Close()
	if err := database.InitDB(path); err != nil {
		t.Fatal(err)
	}
	db := database.GetDB()
	sqlDB, _ := db.DB()
	t.Cleanup(func() { sqlDB.Close() })
	var node model.ManagedNode
	if err := db.First(&node, 1).Error; err != nil || node.TrafficMultiplier == nil || *node.TrafficMultiplier != 1 {
		t.Fatalf("legacy multiplier: %+v %v", node, err)
	}
	s := new(ServerManagementService)
	if err := db.Model(&node).Update("url", "https://example.com").Error; err != nil {
		t.Fatal(err)
	}
	input := NodeInput{ID: 1, Name: "legacy", URL: "https://example.com", Address: "example.com", Token: strings.Repeat("a", 64), TrafficMultiplier: multiplier(0)}
	if err := s.SaveNode(input); err != nil {
		t.Fatal(err)
	}
	input.TrafficMultiplier = nil
	if err := s.SaveNode(input); err != nil {
		t.Fatal(err)
	}
	var saved model.ManagedNode
	if err := db.First(&saved, 1).Error; err != nil || saved.TrafficMultiplier == nil || *saved.TrafficMultiplier != 0 {
		t.Fatalf("zero/omitted setting: %+v %v", saved, err)
	}
	input.TrafficMultiplier = multiplier(-0.1)
	if s.SaveNode(input) == nil {
		t.Fatal("negative multiplier accepted")
	}
}

func TestTrafficMultiplierMonthlyArchiveIsFrozen(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "archive.db")); err != nil {
		t.Fatal(err)
	}
	db := database.GetDB()
	sqlDB, _ := db.DB()
	t.Cleanup(func() { sqlDB.Close() })
	agent := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/traffic/reset") {
			w.Write([]byte(`{"up":70,"down":30}`))
			return
		}
		w.Write([]byte(`{"applied":true}`))
	}))
	defer agent.Close()
	hash := sha256.Sum256(agent.Certificate().Raw)
	node := model.ManagedNode{Name: "scaled", URL: agent.URL, Token: strings.Repeat("a", 64), CertSHA256: hex.EncodeToString(hash[:]), TrafficMultiplier: multiplier(2)}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	in := model.Inbound{Port: 32101, Tag: "inbound-32101", Protocol: model.Trojan, Settings: `{}`, StreamSettings: `{}`, Sniffing: `{}`, MonthlyReset: true}
	if err := db.Create(&in).Error; err != nil {
		t.Fatal(err)
	}
	period := previousMonth(time.Now().In(common.ShanghaiLocation))
	if err := setValue(db, managerMonthKey, strconv.Itoa(period)); err != nil {
		t.Fatal(err)
	}
	s := new(ServerManagementService)
	if err := s.MaybeMonthlyReset(); err != nil {
		t.Fatal(err)
	}
	var archive model.TrafficSnapshot
	if err := db.Where("inbound_id = ? AND yyyymm = ?", in.Id, period).First(&archive).Error; err != nil || archive.RemoteUp != 140 || archive.RemoteDown != 60 {
		t.Fatalf("archive: %+v %v", archive, err)
	}
	if err := db.Model(&node).Update("traffic_multiplier", 0.1).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.MaybeMonthlyReset(); err != nil {
		t.Fatal(err)
	}
	var reread model.TrafficSnapshot
	if err := db.First(&reread, archive.Id).Error; err != nil || reread.RemoteUp != 140 || reread.RemoteDown != 60 {
		t.Fatalf("historical usage changed: %+v %v", reread, err)
	}
}

func TestTrafficMultiplierEnforcesWeightedSharedLimit(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "limits.db")); err != nil {
		t.Fatal(err)
	}
	db := database.GetDB()
	sqlDB, _ := db.DB()
	t.Cleanup(func() { sqlDB.Close() })
	agent := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"applied":true}`))
	}))
	defer agent.Close()
	hash := sha256.Sum256(agent.Certificate().Raw)
	nodes := []model.ManagedNode{
		{Name: "A", URL: agent.URL, Token: strings.Repeat("a", 64), CertSHA256: hex.EncodeToString(hash[:]), Enabled: true, TrafficMultiplier: multiplier(0.1)},
		{Name: "B", URL: agent.URL, Token: strings.Repeat("b", 64), CertSHA256: hex.EncodeToString(hash[:]), Enabled: true, TrafficMultiplier: multiplier(0.1)},
	}
	for i := range nodes {
		if err := db.Create(&nodes[i]).Error; err != nil {
			t.Fatal(err)
		}
	}
	in := model.Inbound{Port: 32102, Tag: "inbound-32102", Enable: true, Total: 500, Protocol: model.Shadowsocks, Settings: `{"password":"test"}`, StreamSettings: `{}`, Sniffing: `{}`}
	if err := db.Create(&in).Error; err != nil {
		t.Fatal(err)
	}
	for _, n := range nodes {
		r := model.NodeTraffic{NodeID: n.Id, AccountID: in.Id, Port: in.Port, Up: 200, Down: 100, Enabled: true, ObservedAt: time.Now().Unix(), ManagerRevision: nodeInboundRevision(&in, n.Id)}
		if err := db.Create(&r).Error; err != nil {
			t.Fatal(err)
		}
	}
	s := new(ServerManagementService)
	if err := s.enforceLimits(); err != nil {
		t.Fatal(err)
	}
	db.First(&in, in.Id)
	if !in.Enable {
		t.Fatal("raw usage 600 should not disable weighted usage 60 below limit 500")
	}
	db.Model(&nodes[0]).Update("traffic_multiplier", 2)
	summary, err := s.Summary(in.Id)
	if err != nil || len(summary) != 1 || summary[0].Total != 630 || !summary[0].Overlimit {
		t.Fatalf("weighted overlimit: %+v %v", summary[0], err)
	}
	if err := s.enforceLimits(); err != nil {
		t.Fatal(err)
	}
	db.First(&in, in.Id)
	if in.Enable || in.DisabledBy != "limit" {
		t.Fatalf("weighted usage must disable: %+v", in)
	}
	summary, err = s.Summary(in.Id)
	if err != nil || summary[0].Status != TrafficStatusOverlimit {
		t.Fatalf("disabled account must report overlimit: %+v %v", summary[0], err)
	}
	var tasks []model.SyncTask
	db.Where("account_id = ? AND kind = ?", in.Id, "disable").Find(&tasks)
	if len(tasks) != 2 {
		t.Fatalf("must disable both target nodes: %+v", tasks)
	}
	// Local-only node details also expose raw traffic and weighted usage.
	r := model.NodeTraffic{NodeID: nodes[0].Id, AccountID: -22, Port: 32103, Up: 100, Down: 50, Enabled: true, ObservedAt: time.Now().Unix()}
	if err := db.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	rows, err := s.Summary(0)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range rows {
		if row.LocalInboundID == 22 {
			found = true
			if row.ActualUsed != 150 || row.Total != 300 || row.Up != 100 || row.Down != 50 || row.TrafficMultiplier != 2 {
				t.Fatalf("local detail: %+v", row)
			}
		}
	}
	if !found {
		t.Fatal("missing local-only detail")
	}
}
