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

func testNodeInput(server *httptest.Server, enabled bool) NodeInput {
	pin := sha256.Sum256(server.Certificate().Raw)
	return NodeInput{Name: "node", URL: server.URL, Address: "node.example.com", Token: strings.Repeat("t", 64), CertSHA256: hex.EncodeToString(pin[:]), Enabled: enabled}
}

func TestMissingDisableRepairsCurrentAccountState(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "manager.db")); err != nil {
		t.Fatal(err)
	}
	var repaired model.Inbound
	agent := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/capabilities":
			w.Write([]byte(`{"apiVersion":1}`))
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/disable"):
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":"inbound not found"}`))
		case r.Method == http.MethodPut:
			if err := json.NewDecoder(r.Body).Decode(&repaired); err != nil {
				t.Error(err)
			}
			w.Write([]byte(`{"applied":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer agent.Close()
	s := new(ServerManagementService)
	if err := s.SaveNode(testNodeInput(agent, true)); err != nil {
		t.Fatal(err)
	}
	nodes, _ := s.Nodes()
	in := model.Inbound{Port: 43339, Protocol: model.Trojan, Settings: `{}`, StreamSettings: `{}`, Sniffing: `{}`, Tag: "inbound-43339", DisabledBy: "limit"}
	if err := database.GetDB().Create(&in).Error; err != nil {
		t.Fatal(err)
	}
	if err := enqueue(database.GetDB(), nodes[0].Id, &in, "disable", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchPending(); err != nil {
		t.Fatal(err)
	}
	if repaired.ManagerAccountID != in.Id || repaired.Enable || repaired.DisabledBy != "limit" {
		t.Fatalf("missing disable was not repaired with disabled account: %+v", repaired)
	}
	pending, _ := s.PendingTasks()
	if len(pending) != 0 {
		t.Fatalf("repair remains pending: %+v", pending)
	}
	changed := testNodeInput(agent, true)
	changed.ID = nodes[0].Id
	changed.URL = "https://replacement.example.com"
	if err := s.SaveNode(changed); err == nil {
		t.Fatal("existing API URL was changed without deleting the old node")
	}
}

func TestCorrectedUpsertReplacesFailedPendingPayload(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "manager.db")); err != nil {
		t.Fatal(err)
	}
	var received []string
	agent := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/capabilities" {
			w.Write([]byte(`{"apiVersion":1}`))
			return
		}
		if r.Method != http.MethodPut {
			http.NotFound(w, r)
			return
		}
		var in model.Inbound
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			t.Error(err)
		}
		received = append(received, in.Remark)
		if in.Remark == "invalid" {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":"invalid configuration"}`))
			return
		}
		w.Write([]byte(`{"applied":true}`))
	}))
	defer agent.Close()
	s := new(ServerManagementService)
	if err := s.SaveNode(testNodeInput(agent, true)); err != nil {
		t.Fatal(err)
	}
	in := model.Inbound{Port: 43339, Protocol: model.Trojan, Settings: `{}`, StreamSettings: `{}`, Sniffing: `{}`, Tag: "inbound-43339", Remark: "invalid"}
	if err := database.GetDB().Create(&in).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.SyncInbound(&in, 0, true); err == nil {
		t.Fatal("invalid upsert unexpectedly succeeded")
	}
	in.Remark = "corrected"
	if err := database.GetDB().Model(&in).Update("remark", in.Remark).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.SyncInbound(&in, in.Port, false); err != nil {
		t.Fatal(err)
	}
	if len(received) != 2 || received[0] != "invalid" || received[1] != "corrected" {
		t.Fatalf("stale configuration was replayed: %v", received)
	}
	pending, err := s.PendingTasks()
	if err != nil || len(pending) != 0 {
		t.Fatalf("corrected upsert remains pending: %v %v", pending, err)
	}
}

func TestFailedAccountDoesNotBlockOtherAccountsOnSameNode(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "manager.db")); err != nil {
		t.Fatal(err)
	}
	var received []int
	agent := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/capabilities" {
			w.Write([]byte(`{"apiVersion":1}`))
			return
		}
		var in model.Inbound
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			t.Error(err)
		}
		received = append(received, in.Port)
		if in.Port == 43339 {
			w.WriteHeader(http.StatusBadRequest)
			w.Write([]byte(`{"error":"invalid configuration"}`))
			return
		}
		w.Write([]byte(`{"applied":true}`))
	}))
	defer agent.Close()
	s := new(ServerManagementService)
	if err := s.SaveNode(testNodeInput(agent, true)); err != nil {
		t.Fatal(err)
	}
	nodes, err := s.Nodes()
	if err != nil {
		t.Fatal(err)
	}
	for _, port := range []int{43339, 43340} {
		in := model.Inbound{Port: port, Protocol: model.Trojan, Settings: `{}`, StreamSettings: `{}`, Sniffing: `{}`, Tag: "inbound-" + strconv.Itoa(port)}
		if err := database.GetDB().Create(&in).Error; err != nil {
			t.Fatal(err)
		}
		if err := enqueue(database.GetDB(), nodes[0].Id, &in, "upsert", ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.DispatchPending(); err == nil {
		t.Fatal("failed first account was reported as success")
	}
	if len(received) != 2 || received[0] != 43339 || received[1] != 43340 {
		t.Fatalf("other account did not progress: %v", received)
	}
	pending, err := s.PendingTasks()
	if err != nil || len(pending) != 1 || pending[0].Port != 43339 {
		t.Fatalf("unexpected pending tasks: %v %v", pending, err)
	}
}

func TestCorrectedUpsertKeepsMonthlyResetBarrier(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "manager.db")); err != nil {
		t.Fatal(err)
	}
	in := model.Inbound{Port: 43339, Protocol: model.Trojan, Settings: `{}`, StreamSettings: `{}`, Sniffing: `{}`, Tag: "inbound-43339", MonthlyReset: true, Remark: "old"}
	if err := database.GetDB().Create(&in).Error; err != nil {
		t.Fatal(err)
	}
	if err := enqueue(database.GetDB(), 1, &in, "upsert", ""); err != nil {
		t.Fatal(err)
	}
	if err := enqueue(database.GetDB(), 1, &in, "reset", "reset-202608-1-1"); err != nil {
		t.Fatal(err)
	}
	in.Remark = "corrected"
	if err := enqueue(database.GetDB(), 1, &in, "upsert", ""); err != nil {
		t.Fatal(err)
	}
	var pending []model.SyncTask
	if err := database.GetDB().Order("id").Find(&pending).Error; err != nil {
		t.Fatal(err)
	}
	if len(pending) != 3 || pending[0].Kind != "upsert" || pending[1].Kind != "reset" || pending[2].Kind != "upsert" {
		t.Fatalf("reset barrier was lost: %+v", pending)
	}
	for _, task := range []model.SyncTask{pending[0], pending[2]} {
		var payload model.Inbound
		if err := json.Unmarshal([]byte(task.Payload), &payload); err != nil || payload.Remark != "corrected" {
			t.Fatalf("stale upsert payload: %+v %v", payload, err)
		}
	}
}

func TestDisabledNodeTasksDoNotFillDispatchPage(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "manager.db")); err != nil {
		t.Fatal(err)
	}
	var upserts int
	agent := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/capabilities" {
			w.Write([]byte(`{"apiVersion":1}`))
			return
		}
		if r.Method == http.MethodPut {
			upserts++
			w.Write([]byte(`{"applied":true}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer agent.Close()
	s := new(ServerManagementService)
	if err := s.SaveNode(testNodeInput(agent, false)); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveNode(testNodeInput(agent, true)); err != nil {
		t.Fatal(err)
	}
	nodes, err := s.Nodes()
	if err != nil {
		t.Fatal(err)
	}
	skipped := make([]model.SyncTask, 500)
	for i := range skipped {
		skipped[i] = model.SyncTask{NodeID: nodes[0].Id, AccountID: i + 1, Port: 20000 + i, Kind: "delete", Payload: `{"accountId":1}`, OperationID: "disabled-" + strconv.Itoa(i), Status: "pending"}
	}
	if err := database.GetDB().CreateInBatches(skipped, 100).Error; err != nil {
		t.Fatal(err)
	}
	in := model.Inbound{Port: 43339, Protocol: model.Trojan, Settings: `{}`, StreamSettings: `{}`, Sniffing: `{}`, Tag: "inbound-43339"}
	if err := database.GetDB().Create(&in).Error; err != nil {
		t.Fatal(err)
	}
	if err := enqueue(database.GetDB(), nodes[1].Id, &in, "upsert", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchPending(); err != nil || upserts != 1 {
		t.Fatalf("enabled node was starved: calls=%d err=%v", upserts, err)
	}
}

func TestManualResetEnablesAccountOnDisabledNode(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "manager.db")); err != nil {
		t.Fatal(err)
	}
	var operations []string
	agent := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/traffic/reset"):
			operations = append(operations, "reset")
			w.Write([]byte(`{"up":70,"down":30}`))
		case r.Method == http.MethodPut:
			var in model.Inbound
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Error(err)
			}
			if !in.Enable || in.DisabledBy != "" {
				t.Errorf("account was not enabled: %+v", in)
			}
			operations = append(operations, "enable")
			w.Write([]byte(`{"applied":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer agent.Close()
	s := new(ServerManagementService)
	if err := s.SaveNode(testNodeInput(agent, false)); err != nil {
		t.Fatal(err)
	}
	in := model.Inbound{Port: 43339, Protocol: model.Trojan, Settings: `{}`, StreamSettings: `{}`, Sniffing: `{}`, Tag: "inbound-43339", Enable: false, DisabledBy: "limit"}
	if err := database.GetDB().Create(&in).Error; err != nil {
		t.Fatal(err)
	}
	nodes, err := s.Nodes()
	if err != nil {
		t.Fatal(err)
	}
	reading := model.NodeTraffic{NodeID: nodes[0].Id, AccountID: in.Id, Port: in.Port, Up: 70, Down: 30}
	if err := database.GetDB().Create(&reading).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.ResetAccountTraffic(in.Id); err != nil {
		t.Fatal(err)
	}
	if len(operations) != 2 || operations[0] != "reset" || operations[1] != "enable" {
		t.Fatalf("manual reset did not enable disabled node account: %v", operations)
	}
	if err := database.GetDB().First(&in, in.Id).Error; err != nil || !in.Enable || in.DisabledBy != "" {
		t.Fatalf("manager account remains disabled: %+v %v", in, err)
	}
	if err := database.GetDB().First(&reading, reading.Id).Error; err != nil || reading.Up != 0 || reading.Down != 0 {
		t.Fatalf("pre-reset cache remained: %+v %v", reading, err)
	}
}

func TestManualResetRecreatesMissingRemoteAccount(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "manager.db")); err != nil {
		t.Fatal(err)
	}
	created := false
	agent := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/traffic/reset"):
			if !created {
				w.WriteHeader(http.StatusNotFound)
				w.Write([]byte(`{"error":"inbound not found"}`))
				return
			}
			w.Write([]byte(`{"up":0,"down":0}`))
		case r.Method == http.MethodPut:
			created = true
			w.Write([]byte(`{"applied":true}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer agent.Close()
	s := new(ServerManagementService)
	if err := s.SaveNode(testNodeInput(agent, false)); err != nil {
		t.Fatal(err)
	}
	in := model.Inbound{Port: 43339, Protocol: model.Trojan, Settings: `{}`, StreamSettings: `{}`, Sniffing: `{}`, Tag: "inbound-43339"}
	if err := database.GetDB().Create(&in).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.ResetAccountTraffic(in.Id); err != nil || !created {
		t.Fatalf("missing remote account was not created and reset: created=%v err=%v", created, err)
	}
}

func TestPendingManualResetDoesNotSuspendLimitEnforcement(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "manager.db")); err != nil {
		t.Fatal(err)
	}
	agent := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"error":"temporarily unavailable"}`))
	}))
	defer agent.Close()
	s := new(ServerManagementService)
	if err := s.SaveNode(testNodeInput(agent, false)); err != nil {
		t.Fatal(err)
	}
	nodes, err := s.Nodes()
	if err != nil {
		t.Fatal(err)
	}
	in := model.Inbound{Port: 43339, Protocol: model.Trojan, Settings: `{}`, StreamSettings: `{}`, Sniffing: `{}`, Tag: "inbound-43339", Total: 50, Enable: false, DisabledBy: "limit"}
	if err := database.GetDB().Create(&in).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().Create(&model.NodeTraffic{NodeID: nodes[0].Id, AccountID: in.Id, Port: in.Port, Up: 100}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.ResetAccountTraffic(in.Id); err == nil {
		t.Fatal("remote reset failure was reported as success")
	}
	if err := s.enforceLimits(); err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().First(&in, in.Id).Error; err != nil || in.Enable || in.DisabledBy != "limit" {
		t.Fatalf("pending reset incorrectly suspended limit enforcement: %+v %v", in, err)
	}
}

func TestMonthlyArchiveIncludesDisabledNode(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "manager.db")); err != nil {
		t.Fatal(err)
	}
	agent := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/traffic/reset") {
			w.Write([]byte(`{"up":70,"down":30}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer agent.Close()
	s := new(ServerManagementService)
	if err := s.SaveNode(testNodeInput(agent, false)); err != nil {
		t.Fatal(err)
	}
	in := model.Inbound{Port: 43339, Protocol: model.Trojan, Settings: `{}`, StreamSettings: `{}`, Sniffing: `{}`, Tag: "inbound-43339", MonthlyReset: true}
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
	var snapshot model.TrafficSnapshot
	if err := database.GetDB().Where("inbound_id = ? AND yyyymm = ?", in.Id, period).First(&snapshot).Error; err != nil || snapshot.RemoteUp != 70 || snapshot.RemoteDown != 30 {
		t.Fatalf("disabled node's monthly usage was omitted: %+v %v", snapshot, err)
	}
}

func TestLateMonthlyRetryUpdatesAlreadyClosedArchive(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "manager.db")); err != nil {
		t.Fatal(err)
	}
	var available atomic.Bool
	agent := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/traffic/reset") {
			if !available.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
				w.Write([]byte(`{"error":"offline"}`))
				return
			}
			w.Write([]byte(`{"up":70,"down":30}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer agent.Close()
	s := new(ServerManagementService)
	if err := s.SaveNode(testNodeInput(agent, false)); err != nil {
		t.Fatal(err)
	}
	in := model.Inbound{Port: 43339, Protocol: model.Trojan, Settings: `{}`, StreamSettings: `{}`, Sniffing: `{}`, Tag: "inbound-43339", MonthlyReset: true}
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
	var archive model.TrafficSnapshot
	if err := database.GetDB().Where("inbound_id = ? AND yyyymm = ?", in.Id, period).First(&archive).Error; err != nil || archive.RemoteUp != 0 {
		t.Fatalf("month did not close around unavailable node: %+v %v", archive, err)
	}
	available.Store(true)
	if err := database.GetDB().Model(&model.SyncTask{}).Where("kind = ? AND status = ?", "reset", "pending").Update("next_retry_at", 0).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.DispatchPending(); err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().First(&archive, archive.Id).Error; err != nil || archive.RemoteUp != 70 || archive.RemoteDown != 30 {
		t.Fatalf("late snapshot did not update archive: %+v %v", archive, err)
	}
}

func TestFailedTaskRetriesTwelveTimesThenAbandonsDependents(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "manager.db")); err != nil {
		t.Fatal(err)
	}
	agent := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"error":"offline"}`))
	}))
	defer agent.Close()
	s := new(ServerManagementService)
	if err := s.SaveNode(testNodeInput(agent, false)); err != nil {
		t.Fatal(err)
	}
	nodes, _ := s.Nodes()
	in := model.Inbound{Port: 43339, Protocol: model.Trojan, Settings: `{}`, StreamSettings: `{}`, Sniffing: `{}`, Tag: "inbound-43339"}
	if err := database.GetDB().Create(&in).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.ResetAccountTraffic(in.Id); err == nil {
		t.Fatal("offline reset unexpectedly succeeded")
	}
	for retry := 1; retry <= syncMaxRetries; retry++ {
		if err := database.GetDB().Model(&model.SyncTask{}).Where("node_id = ? AND kind = ? AND status = ?", nodes[0].Id, "reset", "pending").Update("next_retry_at", 0).Error; err != nil {
			t.Fatal(err)
		}
		_ = s.DispatchPending()
	}
	var tasks []model.SyncTask
	if err := database.GetDB().Where("account_id = ?", in.Id).Order("id").Find(&tasks).Error; err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 2 || tasks[0].Status != "abandoned" || tasks[0].Attempts != syncMaxRetries+1 || tasks[1].Status != "abandoned" {
		t.Fatalf("retry limit or dependent abandonment failed: %+v", tasks)
	}
}

func TestDeleteCancelsPendingEnableOnDisabledNode(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "manager.db")); err != nil {
		t.Fatal(err)
	}
	agent := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"error":"offline"}`))
	}))
	defer agent.Close()
	s := new(ServerManagementService)
	if err := s.SaveNode(testNodeInput(agent, false)); err != nil {
		t.Fatal(err)
	}
	in := model.Inbound{Port: 43339, Protocol: model.Trojan, Settings: `{}`, StreamSettings: `{}`, Sniffing: `{}`, Tag: "inbound-43339"}
	if err := database.GetDB().Create(&in).Error; err != nil {
		t.Fatal(err)
	}
	_ = s.ResetAccountTraffic(in.Id)
	if err := database.GetDB().Delete(&in).Error; err != nil {
		t.Fatal(err)
	}
	_ = s.DeleteSyncedInbound(&in)
	var old []model.SyncTask
	if err := database.GetDB().Where("account_id = ? AND kind IN ?", in.Id, []string{"reset", "enable"}).Find(&old).Error; err != nil {
		t.Fatal(err)
	}
	if len(old) != 2 || old[0].Status != "abandoned" || old[1].Status != "abandoned" {
		t.Fatalf("deleted account can be revived by old tasks: %+v", old)
	}
}

func TestDisabledNodeFinishesOutstandingMonthlyReset(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "manager.db")); err != nil {
		t.Fatal(err)
	}
	agent := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.HasSuffix(r.URL.Path, "/traffic/reset") {
			w.Write([]byte(`{"up":70,"down":30}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer agent.Close()
	s := new(ServerManagementService)
	if err := s.SaveNode(testNodeInput(agent, false)); err != nil {
		t.Fatal(err)
	}
	nodes, _ := s.Nodes()
	in := model.Inbound{Port: 43339, Protocol: model.Trojan, Settings: `{}`, StreamSettings: `{}`, Sniffing: `{}`, Tag: "inbound-43339", MonthlyReset: true}
	if err := database.GetDB().Create(&in).Error; err != nil {
		t.Fatal(err)
	}
	period := previousMonth(time.Now().In(common.ShanghaiLocation))
	if err := setValue(database.GetDB(), managerMonthKey, strconv.Itoa(period)); err != nil {
		t.Fatal(err)
	}
	if err := enqueue(database.GetDB(), nodes[0].Id, &in, "reset", "reset-"+strconv.Itoa(period)+"-1-1"); err != nil {
		t.Fatal(err)
	}
	if err := s.ForceDeleteNode(nodes[0].Id); err == nil {
		t.Fatal("force removal discarded a monthly reset")
	}
	if err := s.MaybeMonthlyReset(); err != nil {
		t.Fatal(err)
	}
	page, err := s.TrafficResetSnapshots(in.Id)
	if err != nil || len(page.Rows) != 1 || page.Rows[0].Remote != 100 {
		t.Fatalf("disabled node's monthly usage missing: page=%+v err=%v", page, err)
	}
	if err := s.ForceDeleteNode(nodes[0].Id); err != nil {
		t.Fatal(err)
	}
	page, err = s.TrafficResetSnapshots(in.Id)
	if err != nil || page.Rows[0].NodeUsed["node (#1)"] != 100 {
		t.Fatalf("historical node disappeared after removal: page=%+v err=%v", page, err)
	}
}

func TestHistoricalNodeUsageIsSeparatedByMonth(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "history.db")); err != nil {
		t.Fatal(err)
	}
	for _, row := range []model.TrafficSnapshot{
		{InboundId: 7, Yyyymm: 202607, Port: 43339, RemoteUp: 20},
		{InboundId: 7, Yyyymm: 202608, Port: 43339, RemoteUp: 30},
	} {
		if err := database.GetDB().Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, row := range []model.NodeTrafficSnapshot{
		{NodeID: 9, NodeName: "old-name", AccountID: 7, Yyyymm: 202607, Up: 20},
		{NodeID: 9, NodeName: "new-name", AccountID: 7, Yyyymm: 202608, Up: 30},
	} {
		if err := database.GetDB().Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	page, err := new(ServerManagementService).TrafficResetSnapshots(7)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Rows) != 2 || page.Rows[0].NodeUsed["new-name (#9)"] != 30 || page.Rows[1].NodeUsed["old-name (#9)"] != 20 {
		t.Fatalf("cross-month aggregation or historical names wrong: %+v", page.Rows)
	}
}

func TestUnrelatedSyncFailureDoesNotBlockCompletedMonth(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "month.db")); err != nil {
		t.Fatal(err)
	}
	agent := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/capabilities":
			w.Write([]byte(`{"apiVersion":1}`))
		case strings.HasSuffix(r.URL.Path, "/traffic/reset"):
			w.Write([]byte(`{"up":10,"down":5}`))
		case r.Method == http.MethodPut:
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(`{"error":"temporary"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer agent.Close()
	s := new(ServerManagementService)
	if err := s.SaveNode(testNodeInput(agent, true)); err != nil {
		t.Fatal(err)
	}
	nodes, _ := s.Nodes()
	period := previousMonth(time.Now().In(common.ShanghaiLocation))
	if err := setValue(database.GetDB(), managerMonthKey, strconv.Itoa(period)); err != nil {
		t.Fatal(err)
	}
	monthly := model.Inbound{Port: 43339, Protocol: model.Trojan, Settings: `{}`, StreamSettings: `{}`, Sniffing: `{}`, Tag: "inbound-43339", MonthlyReset: true}
	other := model.Inbound{Port: 43340, Protocol: model.Trojan, Settings: `{}`, StreamSettings: `{}`, Sniffing: `{}`, Tag: "inbound-43340"}
	for _, in := range []*model.Inbound{&monthly, &other} {
		if err := database.GetDB().Create(in).Error; err != nil {
			t.Fatal(err)
		}
	}
	resetID := "reset-" + strconv.Itoa(period) + "-" + strconv.Itoa(nodes[0].Id) + "-" + strconv.Itoa(monthly.Id)
	if err := enqueue(database.GetDB(), nodes[0].Id, &monthly, "reset", resetID); err != nil {
		t.Fatal(err)
	}
	if err := enqueue(database.GetDB(), nodes[0].Id, &other, "upsert", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.MaybeMonthlyReset(); err != nil {
		t.Fatalf("unrelated sync failure blocked completed month: %v", err)
	}
	page, err := s.TrafficResetSnapshots(monthly.Id)
	if err != nil || len(page.Rows) != 1 || page.Rows[0].Remote != 15 {
		t.Fatalf("monthly snapshot missing: page=%+v err=%v", page, err)
	}
	pending, _ := s.PendingTasks()
	if len(pending) != 1 || pending[0].Kind != "upsert" {
		t.Fatalf("unrelated failure was incorrectly discarded: %+v", pending)
	}
}
