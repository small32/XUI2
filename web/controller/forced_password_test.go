package controller

import (
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
)

func TestReservedAdminMustChangePasswordBeforeManagement(t *testing.T) {
	gin.SetMode(gin.TestMode)
	dbPath := filepath.Join(t.TempDir(), "forced-change.db")
	if err := database.InitDB(dbPath); err != nil {
		t.Fatal(err)
	}
	initial, err := database.ReservedAdminPassword()
	if err != nil {
		t.Fatal(err)
	}
	base := &BaseController{}
	index := &IndexController{}
	engine := gin.New()
	engine.Use(sessions.Sessions("session", cookie.NewStore([]byte("test-secret"))))
	engine.Use(func(c *gin.Context) { c.Set("base_path", "/"); c.Next() })
	index.initRouter(engine.Group("/"))
	engine.Group("/xui").Use(base.checkLogin).GET("/", func(c *gin.Context) { c.String(200, "panel") })
	engine.Group("/server").Use(base.checkAdminLogin).POST("/status", func(c *gin.Context) { c.String(200, "status") })

	request := func(method, path string, body url.Values, session *http.Cookie) *httptest.ResponseRecorder {
		var reader *strings.Reader
		if body == nil {
			reader = strings.NewReader("")
		} else {
			reader = strings.NewReader(body.Encode())
		}
		req := httptest.NewRequest(method, path, reader)
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if session != nil {
			req.AddCookie(session)
		}
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, req)
		return w
	}

	login := request(http.MethodPost, "/login", url.Values{"username": {model.RootUsername}, "password": {initial}}, nil)
	if !strings.Contains(login.Body.String(), `"success":true`) || len(login.Result().Cookies()) == 0 {
		t.Fatalf("initial login failed: %s", login.Body.String())
	}
	oldCookie := login.Result().Cookies()[0]
	if got := request(http.MethodGet, "/xui/", nil, oldCookie); got.Code != http.StatusTemporaryRedirect || got.Header().Get("Location") != "/change-password" {
		t.Fatalf("pending user entered panel: %d %s", got.Code, got.Body.String())
	}
	if got := request(http.MethodPost, "/server/status", nil, oldCookie); got.Body.String() == "status" {
		t.Fatal("pending user reached management API")
	}
	if got := request(http.MethodPost, "/change-password", url.Values{"oldPassword": {initial}, "newPassword": {initial}}, oldCookie); !strings.Contains(got.Body.String(), `"success":false`) {
		t.Fatal("initial password must not be accepted as new password")
	}
	newPassword := "a-unique-password-123"
	changed := request(http.MethodPost, "/change-password", url.Values{"oldPassword": {initial}, "newPassword": {newPassword}}, oldCookie)
	if !strings.Contains(changed.Body.String(), `"success":true`) || len(changed.Result().Cookies()) == 0 {
		t.Fatalf("password change failed: %s", changed.Body.String())
	}
	newCookie := changed.Result().Cookies()[0]
	if got := request(http.MethodGet, "/xui/", nil, newCookie); got.Body.String() != "panel" {
		t.Fatalf("changed user cannot enter panel: %d %s", got.Code, got.Body.String())
	}
	if got := request(http.MethodGet, "/xui/", nil, oldCookie); got.Body.String() == "panel" {
		t.Fatal("old session remains valid after password change")
	}
	if new(service.UserService).CheckUser(model.RootUsername, initial) != nil {
		t.Fatal("initial password remains valid")
	}
	if err := database.InitDB(dbPath); err != nil {
		t.Fatal(err)
	}
	user := new(service.UserService).CheckUser(model.RootUsername, newPassword)
	if user == nil || user.MustChangePassword {
		t.Fatal("changed password was not preserved across initialization")
	}
}
