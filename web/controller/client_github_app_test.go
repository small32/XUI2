package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
	"xui/database"
	"xui/database/model"
	"xui/web/service"
	"xui/web/session"
)

func clientGitHubAppTestDB(t *testing.T) *model.User {
	t.Helper()
	t.Setenv("XUI_ROLE", "manager")
	gin.SetMode(gin.TestMode)
	if err := database.InitDB(filepath.Join(t.TempDir(), "github-app-controller.db")); err != nil {
		t.Fatal(err)
	}
	db, _ := database.GetDB().DB()
	t.Cleanup(func() { db.Close() })
	var admin model.User
	if err := database.GetDB().Where("username = ?", model.AdminUsername).First(&admin).Error; err != nil {
		t.Fatal(err)
	}
	return &admin
}

func clientGitHubAppTestRouter(t *testing.T, login func(*gin.Context)) *gin.Engine {
	t.Helper()
	engine := gin.New()
	engine.Use(sessions.Sessions("session", cookie.NewStore([]byte("github-app-controller-secret-32b"))))
	engine.Use(func(c *gin.Context) {
		c.Set("base_path", "/panel/")
		if login != nil {
			login(c)
		}
		c.Next()
	})
	RegisterClientAdmin(engine.Group("/panel/xui"))
	return engine
}

func clientGitHubAppTestRequest(engine *gin.Engine, method, path string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(method, "https://panel.example"+path, strings.NewReader("{}"))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Requested-With", "XMLHttpRequest")
	engine.ServeHTTP(w, r)
	return w
}

func TestClientGitHubAppRoutesRequireManagerAdmin(t *testing.T) {
	admin := clientGitHubAppTestDB(t)
	paths := []struct{ method, path string }{
		{http.MethodPost, "connect"},
		{http.MethodPost, "status"},
		{http.MethodPost, "disconnect"},
		{http.MethodGet, "callback?state=invalid&code=do-not-echo"},
		{http.MethodGet, "installed?state=invalid"},
	}
	for _, kind := range []string{"anonymous", "restricted", "non-admin"} {
		t.Run(kind, func(t *testing.T) {
			var login func(*gin.Context)
			switch kind {
			case "restricted":
				login = func(c *gin.Context) {
					if err := session.SetRestrictedLogin(c, 7, "password"); err != nil {
						t.Fatal(err)
					}
				}
			case "non-admin":
				user := &model.User{Username: "viewer", Password: "test-hash"}
				if err := database.GetDB().Create(user).Error; err != nil {
					t.Fatal(err)
				}
				login = func(c *gin.Context) {
					if err := session.SetLoginUser(c, user); err != nil {
						t.Fatal(err)
					}
				}
			}
			engine := clientGitHubAppTestRouter(t, login)
			for _, endpoint := range paths {
				w := clientGitHubAppTestRequest(engine, endpoint.method, "/panel/xui/client/github/"+endpoint.path)
				if !strings.Contains(w.Body.String(), `"success":false`) || strings.Contains(w.Body.String(), "do-not-echo") {
					t.Fatalf("%s accessed GitHub %s: %d %s", kind, endpoint.path, w.Code, w.Body.String())
				}
			}
		})
	}
	manager := clientGitHubAppTestRouter(t, func(c *gin.Context) {
		if err := session.SetLoginUser(c, admin); err != nil {
			t.Fatal(err)
		}
	})
	w := clientGitHubAppTestRequest(manager, http.MethodPost, "/panel/xui/client/github/status")
	if !strings.Contains(w.Body.String(), `"success":true`) {
		t.Fatalf("administrator could not query GitHub status: %s", w.Body.String())
	}
	if w := clientGitHubAppTestRequest(manager, http.MethodPost, "/xui/client/github/status"); w.Code != http.StatusNotFound {
		t.Fatal("GitHub App route ignored the panel prefix")
	}
	t.Setenv("XUI_ROLE", "agent")
	agent := gin.New()
	RegisterClientAdmin(agent.Group("/panel/xui"))
	if len(agent.Routes()) != 0 {
		t.Fatal("agent registered GitHub App administration routes")
	}
}

func TestClientGitHubAppRegistrationUsesLoginAddressAndRejectsInvalidState(t *testing.T) {
	admin := clientGitHubAppTestDB(t)
	s := new(service.ClientService)
	settings, err := s.Settings()
	if err != nil {
		t.Fatal(err)
	}
	settings.PublicURL = "https://other-address.example/unrelated/"
	if err := s.SaveSettings(settings); err != nil {
		t.Fatal(err)
	}
	engine := clientGitHubAppTestRouter(t, func(c *gin.Context) {
		if err := session.SetLoginUser(c, admin); err != nil {
			t.Fatal(err)
		}
	})
	w := clientGitHubAppTestRequest(engine, http.MethodPost, "/panel/xui/client/github/connect")
	var registration struct {
		Success bool `json:"success"`
		Obj     struct {
			Action   string `json:"action"`
			Manifest string `json:"manifest"`
		} `json:"obj"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &registration); err != nil || !registration.Success {
		t.Fatalf("start registration: %d %s", w.Code, w.Body.String())
	}
	action, err := url.Parse(registration.Obj.Action)
	if err != nil || action.Scheme != "https" || action.Hostname() != "github.com" || action.Query().Get("state") == "" {
		t.Fatalf("invalid GitHub registration action: %s", registration.Obj.Action)
	}
	for _, path := range []string{"callback", "installed"} {
		if !strings.Contains(registration.Obj.Manifest, "https://panel.example/panel/xui/client/github/"+path) {
			t.Fatalf("manifest callback did not preserve current login address and prefix: %s", registration.Obj.Manifest)
		}
	}
	if strings.Contains(registration.Obj.Manifest, "other-address.example") {
		t.Fatal("registration used a configured external URL instead of current login address")
	}
	reverseProxy := httptest.NewRequest(http.MethodPost, "http://internal.example/panel/xui/client/github/connect", strings.NewReader(`{"baseUrl":"https://public.example/panel/"}`))
	reverseProxy.Header.Set("Content-Type", "application/json")
	reverseProxy.Header.Set("X-Requested-With", "XMLHttpRequest")
	w = httptest.NewRecorder()
	engine.ServeHTTP(w, reverseProxy)
	if err := json.Unmarshal(w.Body.Bytes(), &registration); err != nil || !registration.Success || !strings.Contains(registration.Obj.Manifest, "https://public.example/panel/xui/client/github/callback") {
		t.Fatalf("registration ignored browser address behind TLS reverse proxy: %s", w.Body.String())
	}
	invalidURL := httptest.NewRequest(http.MethodPost, "https://panel.example/panel/xui/client/github/connect", strings.NewReader(`{"baseUrl":"javascript:alert(1)"}`))
	invalidURL.Header.Set("Content-Type", "application/json")
	w = httptest.NewRecorder()
	engine.ServeHTTP(w, invalidURL)
	if !strings.Contains(w.Body.String(), `"success":false`) {
		t.Fatalf("invalid browser callback URL accepted: %s", w.Body.String())
	}
	for _, path := range []string{
		"callback", "callback?state=invalid", "callback?state=invalid&code=do-not-echo",
		"installed", "installed?state=invalid&installation_id=do-not-echo",
	} {
		w := clientGitHubAppTestRequest(engine, http.MethodGet, "/panel/xui/client/github/"+path)
		if w.Code != http.StatusBadRequest || w.Header().Get("Location") != "" || strings.Contains(w.Body.String(), "do-not-echo") {
			t.Fatalf("invalid authorization state accepted or leaked: %s: %d %s", path, w.Code, w.Body.String())
		}
		if w.Header().Get("Cache-Control") != "no-store" || w.Header().Get("Referrer-Policy") != "no-referrer" {
			t.Fatal("authorization callback may cache or forward authorization parameters")
		}
	}
	w = clientGitHubAppTestRequest(engine, http.MethodPost, "/panel/xui/client/github/disconnect")
	if !strings.Contains(w.Body.String(), `"success":true`) {
		t.Fatalf("disconnect: %s", w.Body.String())
	}
	w = clientGitHubAppTestRequest(engine, http.MethodPost, "/panel/xui/client/github/status")
	if strings.Contains(w.Body.String(), `"pending":true`) || strings.Contains(w.Body.String(), `"connected":true`) {
		t.Fatalf("local registration remained after disconnect: %s", w.Body.String())
	}
}
