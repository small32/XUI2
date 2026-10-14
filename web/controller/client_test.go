package controller

import (
	"encoding/json"
	"github.com/gin-gonic/gin"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"xui/database"
	"xui/database/model"
	"xui/web/service"
)

func TestClientAPIManagerOnlyAndAccountIsolation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("XUI_ROLE", "agent")
	agent := gin.New()
	RegisterClientAPI(agent.Group("/panel/"))
	RegisterClientAdmin(agent.Group("/xui"))
	if len(agent.Routes()) != 0 {
		t.Fatal("agent exposes client endpoints")
	}
	t.Setenv("XUI_ROLE", "manager")
	if err := database.InitDB(filepath.Join(t.TempDir(), "api.db")); err != nil {
		t.Fatal(err)
	}
	db, _ := database.GetDB().DB()
	t.Cleanup(func() { db.Close() })
	in := model.Inbound{Port: 18888, Enable: true, Protocol: model.Shadowsocks, Settings: `{"method":"aes-128-gcm","password":"secret"}`, StreamSettings: `{}`, Sniffing: `{}`, Tag: "inbound-18888", Total: 1000}
	database.GetDB().Create(&in)
	node := model.ManagedNode{Name: "A", Address: "a.example.com", Enabled: true}
	database.GetDB().Create(&node)
	database.GetDB().Create(&model.NodeTraffic{NodeID: node.Id, AccountID: in.Id, Port: in.Port, Up: 10, Enabled: true, ObservedAt: time.Now().Unix()})
	manager := gin.New()
	manager.Use(func(c *gin.Context) { c.Set("base_path", "/panel/"); c.Next() })
	RegisterClientAPI(manager.Group("/panel/"))
	request := func(method, path, body, token string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, "https://manager.example/panel/api/client/v1/"+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		manager.ServeHTTP(w, r)
		return w
	}
	if w := request(http.MethodGet, "account", "", "bad"); w.Code != 401 {
		t.Fatalf("anonymous access: %d", w.Code)
	}
	w := request(http.MethodPost, "auth/login", `{"port":"18888","password":"secret"}`, "")
	var tokens service.ClientTokens
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &tokens) != nil {
		t.Fatalf("login: %s", w.Body.String())
	}
	w = request(http.MethodGet, "account?port=9999", "", tokens.AccessToken)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"port":18888`) {
		t.Fatalf("account isolation: %s", w.Body.String())
	}
	w = request(http.MethodGet, "subscription/clash", "", tokens.AccessToken)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "DOMAIN,manager.example,DIRECT") || w.Header().Get("Subscription-Userinfo") == "" {
		t.Fatalf("subscription: %d %s", w.Code, w.Body.String())
	}

	dir := t.TempDir()
	p := &model.ClientPackage{Platform: "windows", Version: "1.0", Filename: "client.exe", Published: true}
	if err := new(service.ClientService).StorePackage(p, strings.NewReader("installer"), dir); err != nil {
		t.Fatal(err)
	}
	downloadController := &ClientController{packageDir: dir}
	manager.GET("/panel/api/client/v1/files-test/:id", downloadController.download)
	manager.HEAD("/panel/api/client/v1/files-test/:id", downloadController.download)
	path := "files-test/" + strconv.Itoa(p.Id)
	download := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "https://manager.example/panel/api/client/v1/"+path, nil)
	req.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
	req.Header.Set("Range", "bytes=0-2")
	manager.ServeHTTP(download, req)
	if download.Code != 200 || download.Body.String() != "installer" || download.Header().Get("Accept-Ranges") != "none" {
		t.Fatalf("range bypass: %d %s", download.Code, download.Body.String())
	}
	release, ok := service.AcquireClientDownload("account:" + strconv.Itoa(in.Id))
	if !ok {
		t.Fatal("download slot remained locked")
	}
	w = request("GET", path, "", tokens.AccessToken)
	release()
	if w.Code != 429 {
		t.Fatalf("parallel download allowed: %d", w.Code)
	}
	w = request("HEAD", path, "", tokens.AccessToken)
	if w.Code != 200 || w.Body.Len() != 0 {
		t.Fatalf("HEAD: %d %s", w.Code, w.Body.String())
	}
	database.GetDB().Model(p).Update("published", false)
	if w = request("GET", path, "", tokens.AccessToken); w.Code != 404 {
		t.Fatal("unpublished package downloaded")
	}
	if _, err := os.Stat(filepath.Join(dir, p.StorageName)); err != nil {
		t.Fatal(err)
	}
	database.GetDB().Model(&in).Update("expiry_time", time.Now().Add(-time.Hour).UnixMilli())
	if w := request(http.MethodGet, "subscription/clash", "", tokens.AccessToken); w.Code != 403 {
		t.Fatalf("expired subscription allowed: %d", w.Code)
	}
	if w := request(http.MethodPost, "auth/logout", "{}", tokens.AccessToken); w.Code != 200 {
		t.Fatal("logout failed")
	}
	if w := request(http.MethodGet, "account", "", tokens.AccessToken); w.Code != 401 {
		t.Fatal("logged-out token accepted")
	}
}
