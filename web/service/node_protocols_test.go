package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"testing"
	"time"
	"xui/database"
	"xui/database/model"
)

func TestNodeProtocolsPreserveSharedFieldsAndPasswords(t *testing.T) {
	in := model.Inbound{Id: 42, Remark: "shared", Port: 32101, Total: 1000, MonthlyReset: true, ExpiryTime: 12345, Enable: true, Protocol: model.VLESS, Settings: `{"clients":[{"id":"keep-base-uuid"}]}`, StreamSettings: `{}`, Sniffing: `{}`, SharedPassword: "shared-secret"}
	for _, c := range []NodeInboundConfig{
		{Protocol: model.Trojan, Settings: `{"clients":[{"password":"different"},{"password":"other"}]}`, StreamSettings: `{}`, Sniffing: `{}`},
		{Protocol: model.Shadowsocks, Settings: `{"method":"aes-128-gcm","password":"different"}`, StreamSettings: `{}`, Sniffing: `{}`},
		{Protocol: model.Socks, Settings: `{"auth":"noauth","accounts":[{"user":"shared","pass":"different"}]}`, StreamSettings: `{}`, Sniffing: `{}`},
		{Protocol: model.Http, Settings: `{"accounts":[{"user":"shared","pass":"different"}]}`, StreamSettings: `{}`, Sniffing: `{}`},
		{Protocol: model.VMess, Settings: `{"clients":[{"id":"vmess-uuid"}]}`, StreamSettings: `{}`, Sniffing: `{}`},
		{Protocol: model.VLESS, Settings: `{"clients":[{"id":"vless-uuid"}]}`, StreamSettings: `{}`, Sniffing: `{}`},
	} {
		data, _ := json.Marshal(map[string]NodeInboundConfig{"1": c})
		in.NodeConfigs = string(data)
		desired, err := InboundForNode(&in, 1)
		if err != nil {
			t.Fatal(err)
		}
		if desired.Id != in.Id || desired.Port != in.Port || desired.Remark != in.Remark || desired.Total != in.Total || desired.MonthlyReset != in.MonthlyReset || desired.ExpiryTime != in.ExpiryTime || desired.Protocol != c.Protocol {
			t.Fatalf("shared fields changed: %+v", desired)
		}
		check := *desired
		check.SharedPassword = ""
		if c.Protocol != model.VLESS && c.Protocol != model.VMess && inboundPassword(&check) != "shared-secret" {
			t.Fatalf("password was not unified: %s", desired.Settings)
		}
		if c.Protocol == model.VLESS || c.Protocol == model.VMess {
			if desired.Settings != c.Settings {
				t.Fatal("UUID configuration was changed")
			}
		}
	}
}

func TestDifferentNodeProtocolsSurviveDispatchReconciliationAndSubscription(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "protocols.db")); err != nil {
		t.Fatal(err)
	}
	db := database.GetDB()
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	actual := map[string]model.Inbound{}
	agent := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("Authorization")
		if r.Method == http.MethodPut {
			var in model.Inbound
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Error(err)
			}
			actual[key] = in
			w.Write([]byte(`{"applied":true}`))
			return
		}
		if r.Method == http.MethodGet {
			json.NewEncoder(w).Encode(actual[key])
			return
		}
		t.Errorf("unexpected remote mutation: %s", r.Method)
	}))
	defer agent.Close()
	input := testNodeInput(agent, true)
	var nodes []model.ManagedNode
	for _, token := range []string{"trojan-node", "vless-node"} {
		n := model.ManagedNode{Name: token, Token: token, URL: input.URL, Address: token + ".example", CertSHA256: input.CertSHA256, Enabled: true}
		if err := db.Create(&n).Error; err != nil {
			t.Fatal(err)
		}
		nodes = append(nodes, n)
	}
	configs, _ := json.Marshal(map[string]NodeInboundConfig{strconv.Itoa(nodes[1].Id): {Protocol: model.VLESS, Settings: `{"clients":[{"id":"keep-vless-uuid"}]}`, StreamSettings: `{}`, Sniffing: `{}`}})
	in := model.Inbound{Port: 32101, Tag: "inbound-32101", Remark: "shared", Protocol: model.Trojan, Settings: `{"clients":[{"password":"old"}]}`, StreamSettings: `{}`, Sniffing: `{}`, SharedPassword: "shared-secret", NodeConfigs: string(configs), Enable: true, Total: 1000, MonthlyReset: true, ExpiryTime: 123456}
	if err := ValidateNodeProtocols(&in); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&in).Error; err != nil {
		t.Fatal(err)
	}
	s := new(ServerManagementService)
	if err := s.SyncInbound(&in, 0, true); err != nil {
		t.Fatal(err)
	}
	if actual["Bearer trojan-node"].Protocol != model.Trojan || actual["Bearer vless-node"].Protocol != model.VLESS {
		t.Fatalf("per-node protocols lost: %+v", actual)
	}
	for _, n := range nodes {
		remote := actual["Bearer "+n.Name]
		if remote.ManagerRevision != nodeInboundRevision(&in, n.Id) {
			t.Fatal("per-node revision mismatch")
		}
		if err := s.reconcileNode(n.Id, []agentTraffic{{AccountID: in.Id, Port: in.Port, ManagerRevision: remote.ManagerRevision}}); err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&model.NodeTraffic{NodeID: n.Id, AccountID: in.Id, Port: in.Port, Enabled: true, ObservedAt: time.Now().Unix(), ManagerRevision: remote.ManagerRevision}).Error; err != nil {
			t.Fatal(err)
		}
	}
	var pending int64
	db.Model(&model.SyncTask{}).Where("status = ?", "pending").Count(&pending)
	if pending != 0 {
		t.Fatal("heartbeat overwrote per-node protocols")
	}
	rows, err := s.Summary(0)
	if err != nil || len(rows) != 1 || rows[0].Status == "unknown" {
		t.Fatalf("per-node protocol is incorrectly stale: %+v %v", rows, err)
	}
	remote, err := s.RemoteInbounds(in.Port)
	if err != nil || len(remote) != 2 || remote[0]["protocol"] != "trojan" || remote[1]["protocol"] != "vless" {
		t.Fatalf("subscription did not merge node protocols: %+v %v", remote, err)
	}
	masked, err := WithLoginPassword(model.VLESS, actual["Bearer vless-node"].Settings, "shared-secret")
	if err != nil || masked != actual["Bearer vless-node"].Settings {
		t.Fatal("restricted subscription changed VLESS UUID")
	}
}
