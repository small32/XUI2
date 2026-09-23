package service

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"xui/database"
	"xui/database/model"
	"xui/util/common"
)

func TestManagerSyncAndSharedLimit(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "manager.db")); err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("a", 64)
	var upserts, disables atomic.Int32
	var accountID atomic.Int32
	var revision atomic.Value
	revision.Store("")
	agent := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "unauthorized", 401)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == "GET" && r.URL.Path == "/api/v1/capabilities":
			w.Write([]byte(`{"apiVersion":1}`))
		case r.Method == "PUT" && strings.HasPrefix(r.URL.Path, "/api/v1/inbounds/"):
			var in model.Inbound
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Error(err)
			}
			accountID.Store(int32(in.ManagerAccountID))
			revision.Store(in.ManagerRevision)
			upserts.Add(1)
			w.Write([]byte(`{"applied":true}`))
		case r.Method == "GET" && r.URL.Path == "/api/v1/traffic":
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"inbounds": []map[string]interface{}{{"accountId": accountID.Load(), "port": 34872, "up": 60, "down": 40, "enabled": true, "managerRevision": revision.Load()}}})
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/disable"):
			disables.Add(1)
			w.Write([]byte(`{"applied":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer agent.Close()
	pin := sha256.Sum256(agent.Certificate().Raw)
	s := new(ServerManagementService)
	if err := s.SaveNode(NodeInput{Name: "HK", URL: agent.URL, Address: "hk.example.com", Token: token, CertSHA256: hex.EncodeToString(pin[:]), Enabled: true}); err != nil {
		t.Fatal(err)
	}
	account := model.Inbound{UserId: 1, Port: 34872, Protocol: model.Trojan, Settings: `{"clients":[{"password":"example"}]}`, StreamSettings: `{}`, Sniffing: `{}`, Total: 100, Enable: true, Tag: "inbound-34872"}
	if err := database.GetDB().Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.SyncInbound(&account, 0, true); err != nil {
		t.Fatal(err)
	}
	if upserts.Load() != 1 || int(accountID.Load()) != account.Id {
		t.Fatalf("upsert missing: count=%d id=%d", upserts.Load(), accountID.Load())
	}
	if err := s.MaybeMonthlyReset(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Traffic(); err != nil {
		t.Fatal(err)
	}
	if disables.Load() != 1 {
		t.Fatalf("expected global disable, got %d", disables.Load())
	}
	var stored model.Inbound
	if err := database.GetDB().First(&stored, account.Id).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Enable || stored.DisabledBy != "limit" {
		t.Fatalf("wrong manager state: %+v", stored)
	}
	pending, err := s.PendingTasks()
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending tasks: %v %v", pending, err)
	}
}

func TestAgentMonthlyResetIsIdempotent(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "agent.db")); err != nil {
		t.Fatal(err)
	}
	account := model.Inbound{UserId: 1, ManagerAccountID: 99, Port: 34872, Protocol: model.Trojan, Settings: `{"clients":[]}`, StreamSettings: `{}`, Sniffing: `{}`, MonthlyReset: true, Up: 30, Down: 20, Enable: true, Tag: "inbound-34872"}
	if err := database.GetDB().Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	period := previousMonth(time.Now())
	first, err := ResetAgentTraffic(99, 34872, period, "reset-first-operation")
	if err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().Model(&account).UpdateColumns(map[string]interface{}{"up": 7, "down": 5}).Error; err != nil {
		t.Fatal(err)
	}
	second, err := ResetAgentTraffic(99, 34872, period, "reset-second-operation")
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatalf("snapshot changed: %s vs %s", first, second)
	}
	var stored model.Inbound
	if err := database.GetDB().First(&stored, account.Id).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Up != 7 || stored.Down != 5 {
		t.Fatalf("retry cleared new-period traffic: %+v", stored)
	}
}

func TestManagerRetriesFailedNodeWithoutLosingOtherNode(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "retry.db")); err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("b", 64)
	var fail atomic.Bool
	fail.Store(true)
	var goodCalls, badCalls atomic.Int32
	makeAgent := func(calls *atomic.Int32, shouldFail bool) *httptest.Server {
		return httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer "+token {
				http.Error(w, "unauthorized", 401)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			if r.URL.Path == "/api/v1/capabilities" {
				w.Write([]byte(`{"apiVersion":1}`))
				return
			}
			if r.Method == "PUT" {
				calls.Add(1)
				if shouldFail && fail.Load() {
					http.Error(w, `{"error":"temporary"}`, 503)
					return
				}
				w.Write([]byte(`{"applied":true}`))
				return
			}
			http.NotFound(w, r)
		}))
	}
	good := makeAgent(&goodCalls, false)
	defer good.Close()
	bad := makeAgent(&badCalls, true)
	defer bad.Close()
	add := func(name string, server *httptest.Server) {
		pin := sha256.Sum256(server.Certificate().Raw)
		if err := new(ServerManagementService).SaveNode(NodeInput{Name: name, URL: server.URL, Address: name + ".example.com", Token: token, CertSHA256: hex.EncodeToString(pin[:]), Enabled: true}); err != nil {
			t.Fatal(err)
		}
	}
	add("good", good)
	add("bad", bad)
	account := model.Inbound{UserId: 1, Port: 12345, Protocol: model.Trojan, Settings: `{}`, StreamSettings: `{}`, Sniffing: `{}`, Enable: true, Tag: "inbound-12345"}
	if err := database.GetDB().Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	if err := new(ServerManagementService).SyncInbound(&account, 0, true); err == nil {
		t.Fatal("partial failure was reported as success")
	}
	if goodCalls.Load() != 1 || badCalls.Load() != 1 {
		t.Fatalf("dispatch counts: good=%d bad=%d", goodCalls.Load(), badCalls.Load())
	}
	pending, err := new(ServerManagementService).PendingTasks()
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending=%v err=%v", pending, err)
	}
	fail.Store(false)
	if err := new(ServerManagementService).DispatchPending(); err != nil {
		t.Fatal(err)
	}
	pending, err = new(ServerManagementService).PendingTasks()
	if err != nil || len(pending) != 0 {
		t.Fatalf("pending after retry=%v err=%v", pending, err)
	}
	if goodCalls.Load() != 1 || badCalls.Load() != 2 {
		t.Fatalf("unexpected replay: good=%d bad=%d", goodCalls.Load(), badCalls.Load())
	}
}

func TestManagerMonthlySnapshotUsesAgentResetResult(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "month.db")); err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("c", 64)
	agent := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/capabilities":
			w.Write([]byte(`{"apiVersion":1}`))
		case strings.HasSuffix(r.URL.Path, "/traffic/reset"):
			w.Write([]byte(`{"up":70,"down":30,"port":34872}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer agent.Close()
	pin := sha256.Sum256(agent.Certificate().Raw)
	s := new(ServerManagementService)
	if err := s.SaveNode(NodeInput{Name: "node", URL: agent.URL, Address: "node.example.com", Token: token, CertSHA256: hex.EncodeToString(pin[:]), Enabled: true}); err != nil {
		t.Fatal(err)
	}
	in := model.Inbound{UserId: 1, Port: 34872, Protocol: model.Trojan, Settings: `{}`, StreamSettings: `{}`, Sniffing: `{}`, Enable: true, MonthlyReset: true, Total: 1000, Tag: "inbound-34872"}
	if err := database.GetDB().Create(&in).Error; err != nil {
		t.Fatal(err)
	}
	period := previousMonth(time.Now().In(common.ShanghaiLocation))
	if err := setValue(database.GetDB(), managerMonthKey, strconv.Itoa(period)); err != nil {
		t.Fatal(err)
	}
	if err := s.MaybeMonthlyReset(); err != nil {
		t.Fatal(err)
	}
	rows, err := s.TrafficResetSnapshots(in.Id)
	if err != nil || len(rows) != 1 || rows[0].Remote != 100 || rows[0].Yyyymm != period {
		t.Fatalf("snapshot=%v err=%v", rows, err)
	}
	if err := s.MaybeMonthlyReset(); err != nil {
		t.Fatal(err)
	}
	rows, err = s.TrafficResetSnapshots(in.Id)
	if err != nil || len(rows) != 1 {
		t.Fatalf("duplicate snapshot=%v err=%v", rows, err)
	}
}

func TestTrafficFollowsAccountAfterPortChange(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "port.db")); err != nil {
		t.Fatal(err)
	}
	account := model.Inbound{UserId: 1, Port: 12345, Protocol: model.Trojan, Settings: `{}`, StreamSettings: `{}`, Sniffing: `{}`, Tag: "inbound-12345"}
	if err := database.GetDB().Create(&account).Error; err != nil {
		t.Fatal(err)
	}
	reading := model.NodeTraffic{NodeID: 1, AccountID: account.Id, Port: 12345, Up: 70, Down: 30, Enabled: true, ObservedAt: time.Now().Unix()}
	if err := database.GetDB().Create(&reading).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().Model(&account).Update("port", 23456).Error; err != nil {
		t.Fatal(err)
	}
	rows, err := new(ServerManagementService).GetTrafficCache()
	if err != nil || len(rows) != 1 || rows[0].Port != 23456 || rows[0].Used != 100 {
		t.Fatalf("cache did not follow account: %v %v", rows, err)
	}
}
