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

func TestImportNodeInboundPreservesTargetIdentityAndTraffic(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "copy.db")); err != nil {
		t.Fatal(err)
	}
	sqlDB, err := database.GetDB().DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	var owner model.User
	if err := database.GetDB().Where("username = ?", model.AdminUsername).First(&owner).Error; err != nil {
		t.Fatal(err)
	}
	source := model.Inbound{Id: 999, UserId: 999, ManagerAccountID: 42, ManagerRevision: "source", Port: 32101, Protocol: model.VLESS, Tag: "source", Remark: "customer", Enable: true, Up: 500, Down: 600, Settings: `{"clients":[]}`, StreamSettings: `{}`, Sniffing: `{}`}
	if err := ImportNodeInbound(source, false); err != nil {
		t.Fatal(err)
	}
	var stored model.Inbound
	if err := database.GetDB().Where("port = ?", source.Port).First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Id == source.Id || stored.UserId != owner.Id || stored.ManagerAccountID != 0 || stored.ManagerRevision != "" || stored.Up != 0 || stored.Down != 0 || stored.Tag != "inbound-32101" {
		t.Fatalf("source identity leaked: %+v", stored)
	}
	if err := database.GetDB().Model(&stored).Updates(map[string]interface{}{"up": 123, "down": 456}).Error; err != nil {
		t.Fatal(err)
	}
	source.Remark = "updated"
	if err := ImportNodeInbound(source, false); err == nil {
		t.Fatal("existing port must be skipped")
	}
	if err := ImportNodeInbound(source, true); err != nil {
		t.Fatal(err)
	}
	var updated model.Inbound
	if err := database.GetDB().First(&updated, stored.Id).Error; err != nil {
		t.Fatal(err)
	}
	if updated.Remark != "updated" || updated.Up != 123 || updated.Down != 456 || updated.Id != stored.Id {
		t.Fatalf("overwrite changed traffic or identity: %+v", updated)
	}
	if err := database.GetDB().Model(&updated).Update("manager_account_id", 12).Error; err != nil {
		t.Fatal(err)
	}
	if err := ImportNodeInbound(source, true); err == nil {
		t.Fatal("managed inbound must not be overwritten")
	}
}

func TestCopyNodeInboundsUsesSelectedPortsAndTargets(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "nodes.db")); err != nil {
		t.Fatal(err)
	}
	sqlDB, err := database.GetDB().DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	source := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/inbounds/all" {
			t.Errorf("unexpected read path: %s", r.URL.Path)
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"inbounds": []model.Inbound{
			{Id: 10, ManagerAccountID: 99, ManagerRevision: "source", Port: 32101, Up: 500, Down: 600, Protocol: model.VLESS, Settings: `{}`, StreamSettings: `{}`},
			{Id: 11, Port: 32102, Protocol: model.Trojan, Settings: `{}`, StreamSettings: `{}`},
		}})
	}))
	defer source.Close()
	calls := 0
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/v1/inbounds/all" {
			w.Write([]byte(`{"inbounds":[]}`))
			return
		}
		calls++
		if r.URL.Path != "/api/v1/inbounds/copy/32101" || r.Method != http.MethodPut {
			t.Errorf("wrong selected inbound: %s %s", r.Method, r.URL.Path)
		}
		var payload InboundCopyPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
		}
		if !payload.Overwrite || payload.Inbound.Id != 0 || payload.Inbound.ManagerAccountID != 0 || payload.Inbound.ManagerRevision != "" || payload.Inbound.Up != 0 || payload.Inbound.Down != 0 {
			t.Errorf("incorrect payload: %+v", payload)
		}
		w.Write([]byte(`{"applied":true}`))
	}))
	defer target.Close()
	s := new(ServerManagementService)
	for _, server := range []*httptest.Server{source, target} {
		input := testNodeInput(server, true)
		node := model.ManagedNode{Name: input.Name, URL: input.URL, Token: input.Token, CertSHA256: input.CertSHA256}
		if err := database.GetDB().Create(&node).Error; err != nil {
			t.Fatal(err)
		}
	}
	rows, err := s.AllNodeInbounds()
	if err != nil || len(rows) != 2 || len(rows[0].Inbounds) != 2 {
		t.Fatalf("all-node read failed: %+v %v", rows, err)
	}
	calls = 0
	result, err := s.CopyNodeInbounds(NodeInboundCopyRequest{SourceNodeID: rows[0].NodeID, TargetNodeIDs: []int{rows[1].NodeID, rows[1].NodeID}, Ports: []int{32101, 32101}, Overwrite: true})
	if err != nil || len(result) != 1 || !result[0].Success || calls != 1 {
		t.Fatalf("copy selection failed: %+v %v calls=%d", result, err, calls)
	}
	_, err = s.CopyNodeInbounds(NodeInboundCopyRequest{SourceNodeID: rows[0].NodeID, TargetNodeIDs: []int{rows[1].NodeID}, Ports: []int{65500}})
	if err == nil || calls != 1 {
		t.Fatal("missing source port should fail before writing")
	}
}
