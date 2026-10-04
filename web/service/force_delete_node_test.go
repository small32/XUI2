package service

import (
	"path/filepath"
	"testing"
	"xui/database"
	"xui/database/model"
)

func TestForceDeleteNodeDiscardsUnfinishedMonthlyTasksOffline(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "remove.db")); err != nil {
		t.Fatal(err)
	}
	db := database.GetDB()
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	// An unusable URL/certificate also verifies removal needs no agent request.
	node := model.ManagedNode{Name: "offline", URL: "https://127.0.0.1:1", CertSHA256: "invalid", Enabled: true}
	other := model.ManagedNode{Name: "other"}
	for _, row := range []*model.ManagedNode{&node, &other} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, status := range []string{"pending", "inflight", "abandoned"} {
		if err := db.Create(&model.SyncTask{NodeID: node.Id, Kind: "reset", Status: status, OperationID: status}).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Create(&model.SyncTask{NodeID: other.Id, Kind: "reset", Status: "pending", OperationID: "other"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.NodeTraffic{NodeID: node.Id, AccountID: 42, Up: 50}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&model.NodeTrafficSnapshot{NodeID: node.Id, AccountID: 42, Yyyymm: 202609, Up: 100}).Error; err != nil {
		t.Fatal(err)
	}
	if err := new(ServerManagementService).ForceDeleteNode(node.Id); err != nil {
		t.Fatal(err)
	}
	for _, table := range []interface{}{&model.ManagedNode{}, &model.SyncTask{}, &model.NodeTraffic{}} {
		var count int64
		query := db.Model(table)
		if _, ok := table.(*model.ManagedNode); ok {
			query = query.Where("id = ?", node.Id)
		} else {
			query = query.Where("node_id = ?", node.Id)
		}
		if err := query.Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("node records remain in %T: %d", table, count)
		}
	}
	var snapshot model.NodeTrafficSnapshot
	if err := db.Where("node_id = ?", node.Id).First(&snapshot).Error; err != nil {
		t.Fatal(err)
	}
	if snapshot.Up != 100 || snapshot.NodeName != node.Name {
		t.Fatalf("history changed: %+v", snapshot)
	}
	var count int64
	if err := db.Model(&model.SyncTask{}).Where("node_id = ?", other.Id).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("other node's tasks were removed")
	}
}
