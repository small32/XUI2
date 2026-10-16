package controller

import (
	"encoding/json"
	"github.com/gin-gonic/gin"
	"net/http"
	"net/http/httptest"
	"path/filepath"
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
	RegisterClientAdmin(manager.Group("/panel/xui"))
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

	// Upgraded databases may retain uploaded packages and external links. Neither
	// may appear in the GitHub-only list, even if the old records were published.
	if err := database.GetDB().Exec(`CREATE TABLE IF NOT EXISTS client_packages (
		id INTEGER PRIMARY KEY, platform TEXT, arch TEXT, version TEXT,
		filename TEXT, storage_name TEXT, external_url TEXT, size INTEGER,
		sha256 TEXT, published NUMERIC, created_at INTEGER
	)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.GetDB().Exec(`INSERT INTO client_packages
		(id, platform, arch, version, filename, storage_name, external_url, size, published)
		VALUES (9001, 'windows', 'x64', 'legacy', 'legacy.exe', 'old.exe', '', 9, 1),
		       (9002, 'ios', 'arm64', 'legacy', '', '', 'https://legacy.example/client', 0, 1)`).Error; err != nil {
		t.Fatal(err)
	}
	clientSettings, err := new(service.ClientService).Settings()
	if err != nil {
		t.Fatal(err)
	}
	clientSettings.GitHubReleaseEnable = false
	if err := new(service.ClientService).SaveSettings(clientSettings); err != nil {
		t.Fatal(err)
	}
	w = request(http.MethodGet, "downloads", "", tokens.AccessToken)
	if w.Code != http.StatusOK || strings.TrimSpace(w.Body.String()) != "[]" {
		t.Fatalf("legacy packages leaked into GitHub-only downloads: %d %s", w.Code, w.Body.String())
	}
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		for _, path := range []string{"downloads/9001", "downloads/9002"} {
			if w := request(method, path, "", tokens.AccessToken); w.Code != http.StatusNotFound {
				t.Fatalf("removed local download route still registered: %s %s: %d", method, path, w.Code)
			}
		}
	}
	for _, path := range []string{"packages", "packages/upload", "packages/9001/publish", "packages/9001/delete"} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "https://manager.example/panel/xui/client/"+path, strings.NewReader("{}"))
		r.Header.Set("Content-Type", "application/json")
		manager.ServeHTTP(w, r)
		if w.Code != http.StatusNotFound {
			t.Fatalf("removed package administration route still registered: %s: %d %s", path, w.Code, w.Body.String())
		}
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
