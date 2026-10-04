package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"xui/database"
	"xui/database/model"
)

func TestInboundTargetsLimitDispatchAndHeartbeat(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "targets.db")); err != nil {
		t.Fatal(err)
	}
	db := database.GetDB()
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	calls := map[string]int{}
	agent := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls[r.Header.Get("Authorization")]++
		w.Write([]byte(`{"applied":true}`))
	}))
	defer agent.Close()
	input := testNodeInput(agent, true)
	var nodes []model.ManagedNode
	for _, token := range []string{"first", "second", "third"} {
		node := model.ManagedNode{Name: token, URL: input.URL, Token: token, CertSHA256: input.CertSHA256, Enabled: true}
		if err := db.Create(&node).Error; err != nil {
			t.Fatal(err)
		}
		nodes = append(nodes, node)
	}
	targets, _ := json.Marshal([]int{nodes[0].Id, nodes[2].Id})
	in := model.Inbound{Port: 32101, Tag: "inbound-32101", Protocol: model.VLESS, Settings: `{}`, StreamSettings: `{}`, TargetNodes: string(targets), Enable: true}
	if err := db.Create(&in).Error; err != nil {
		t.Fatal(err)
	}
	s := new(ServerManagementService)
	if err := s.SyncInbound(&in, 0, true); err != nil {
		t.Fatal(err)
	}
	if calls["Bearer first"] != 1 || calls["Bearer third"] != 1 || calls["Bearer second"] != 0 {
		t.Fatalf("dispatch ignored targets: %+v", calls)
	}
	if err := s.reconcileNode(nodes[1].Id, nil); err != nil {
		t.Fatal(err)
	}
	var pending int64
	db.Model(&model.SyncTask{}).Where("node_id = ? AND status = ?", nodes[1].Id, "pending").Count(&pending)
	if pending != 0 {
		t.Fatal("heartbeat reintroduced excluded node")
	}
	for _, kind := range []string{"upsert", "reset", "enable", "disable"} {
		if err := enqueue(db, nodes[1].Id, &in, kind, ""); err != nil {
			t.Fatal(err)
		}
	}
	db.Model(&model.SyncTask{}).Where("node_id = ?", nodes[1].Id).Count(&pending)
	if pending != 0 {
		t.Fatal("maintenance queued work for excluded node")
	}
	copy := in
	copy.ManagerAccountID = in.Id
	copy.ManagerRevision = inboundRevision(&in)
	payload, _ := json.Marshal(copy)
	task := model.SyncTask{NodeID: nodes[1].Id, AccountID: in.Id, Port: in.Port, Kind: "upsert", Payload: string(payload), OperationID: "stale", Status: "pending"}
	if err := db.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchPending(); err != nil {
		t.Fatal(err)
	}
	if calls["Bearer second"] != 0 {
		t.Fatal("stale retry dispatched to excluded node")
	}
	if err := db.First(&task, task.Id).Error; err != nil {
		t.Fatal(err)
	}
	if task.Status != "abandoned" {
		t.Fatal("stale retry not abandoned")
	}
	if err := s.reconcileNode(nodes[1].Id, []agentTraffic{{AccountID: in.Id, Port: in.Port}}); err != nil {
		t.Fatal(err)
	}
	var cleanup model.SyncTask
	if err := db.Where("node_id = ? AND kind = ? AND status = ?", nodes[1].Id, "delete", "pending").First(&cleanup).Error; err != nil {
		t.Fatal("excluded deployed account not queued for cleanup", err)
	}
	if err := db.Model(&in).Update("target_nodes", "").Error; err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchPending(); err != nil {
		t.Fatal(err)
	}
	if calls["Bearer second"] != 0 {
		t.Fatal("reselected node was deleted by an old cleanup task")
	}
	if err := db.First(&cleanup, cleanup.Id).Error; err != nil {
		t.Fatal(err)
	}
	if cleanup.Status != "abandoned" {
		t.Fatal("old cleanup was not cancelled after reselection")
	}
}
