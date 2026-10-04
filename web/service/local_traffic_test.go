package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
	"xui/database"
	"xui/database/model"
)

func TestLocalInboundTrafficIsCollectedWithoutManagedMutations(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "traffic.db")); err != nil {
		t.Fatal(err)
	}
	db := database.GetDB()
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	agent := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v1/traffic" {
			t.Errorf("local inbound caused remote mutation: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(500)
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"inbounds": []agentTraffic{
			{LocalID: 1, Port: 32101, Username: "first", Up: 10, Down: 20, Enabled: true, Total: 100, ExpiryTime: 1234, MonthlyReset: true},
			{LocalID: 2, Port: 32102, Username: "second", Up: 40, Down: 50, Enabled: true},
		}})
	}))
	defer agent.Close()
	input := testNodeInput(agent, true)
	for _, name := range []string{"node-a", "node-b"} {
		if err := db.Create(&model.ManagedNode{Name: name, URL: input.URL, Token: input.Token, CertSHA256: input.CertSHA256, Enabled: true}).Error; err != nil {
			t.Fatal(err)
		}
	}
	s := new(ServerManagementService)
	if _, err := s.Traffic(); err != nil {
		t.Fatal(err)
	}
	rows, err := s.Summary(0)
	if err != nil || len(rows) != 4 {
		t.Fatalf("local rows missing: %+v %v", rows, err)
	}
	for _, row := range rows {
		if row.LocalInboundID <= 0 || row.NodeName == "" || row.InboundId != 0 {
			t.Fatalf("local identity lost: %+v", row)
		}
		if row.LocalInboundID == 1 && (row.Total != 30 || row.Limit != 100 || !row.MonthlyReset || row.ExpiryTime != 1234) {
			t.Fatalf("metadata lost: %+v", row)
		}
	}
	var tasks int64
	db.Model(&model.SyncTask{}).Count(&tasks)
	if tasks != 0 {
		t.Fatal("local inbounds were queued for deletion")
	}
	nodes, err := s.ListNodes()
	if err != nil || len(nodes) != 2 || nodes[0].InboundCount != 2 || nodes[0].Used != 120 {
		t.Fatalf("node totals missing local traffic: %+v %v", nodes, err)
	}
	if err := s.enforceLimits(); err != nil {
		t.Fatalf("managed limit processing touched local rows: %v", err)
	}
}

func TestLocalTrafficEntersSharedAccountTotalsByPort(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "separate.db")); err != nil {
		t.Fatal(err)
	}
	db := database.GetDB()
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	in := model.Inbound{Port: 32101, Tag: "managed", Enable: true, Total: 1000}
	if err := db.Create(&in).Error; err != nil {
		t.Fatal(err)
	}
	node := model.ManagedNode{Name: "node", Enabled: true}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	for _, r := range []model.NodeTraffic{
		{NodeID: node.Id, AccountID: in.Id, Port: in.Port, Up: 10, Enabled: true, ManagerRevision: inboundRevision(&in), ObservedAt: time.Now().Unix()},
		{NodeID: node.Id, AccountID: -in.Id, Port: in.Port, Up: 900, Enabled: true, ObservedAt: time.Now().Unix()},
		{NodeID: node.Id, AccountID: -999, Port: in.Port + 1, Up: 5000, Enabled: true, ObservedAt: time.Now().Unix()},
	} {
		if err := db.Create(&r).Error; err != nil {
			t.Fatal(err)
		}
	}
	s := new(ServerManagementService)
	rows, err := s.Summary(in.Id)
	if err != nil || len(rows) != 1 || rows[0].Total != 910 || rows[0].LocalInboundID != 0 {
		t.Fatalf("same-port local traffic missing from shared total: %+v %v", rows, err)
	}
	all, err := s.Summary(0)
	if err != nil || len(all) != 2 {
		t.Fatalf("matching port not merged or unmatched local row missing: %+v %v", all, err)
	}
	detail, err := s.NodeUsage(in.Port)
	if err != nil || len(detail) != 1 || detail[0].Used != 910 {
		t.Fatalf("node detail disagrees with shared total: %+v %v", detail, err)
	}
	if err := db.Model(&in).Update("total", 900).Error; err != nil {
		t.Fatal(err)
	}
	rows, err = s.Summary(in.Id)
	if err != nil || !rows[0].Overlimit {
		t.Fatalf("same-port local traffic not counted toward quota: %+v %v", rows, err)
	}
}
