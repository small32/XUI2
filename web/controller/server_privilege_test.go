package controller

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"

	"x-ui/database"
	"x-ui/database/model"
	"x-ui/web/session"
)

// 受限登录不得访问 /server 等管理接口：受限会话不建立占位用户，
// checkAdminLogin 应将其拦截，而管理员会话应放行。
// 锁住 #1 越权修复，防止日后改动回归。
func TestRestrictedLoginDeniedOnAdminEndpoints(t *testing.T) {
	gin.SetMode(gin.TestMode)
	if err := database.InitDB(filepath.Join(t.TempDir(), "sessions.db")); err != nil {
		t.Fatal(err)
	}
	admin := &model.User{Username: "admin", Password: "test-hash"}
	if err := database.GetDB().Create(admin).Error; err != nil {
		t.Fatal(err)
	}

	build := func(setupSession func(c *gin.Context)) *gin.Engine {
		engine := gin.New()
		engine.Use(sessions.Sessions("session", cookie.NewStore([]byte("test-secret"))))
		engine.Use(setupSession)
		engine.Group("/").Use((&ServerController{}).checkAdminLogin).POST("/server/status",
			func(c *gin.Context) { c.String(http.StatusOK, "ok") })
		return engine
	}

	// 受限会话：仅绑定入站 id，不建立占位用户。
	restrictedEngine := build(func(c *gin.Context) {
		c.Set("base_path", "/")
		if err := session.SetRestrictedLogin(c, 7, "password"); err != nil {
			t.Fatal(err)
		}
		c.Next()
	})

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/server/status", strings.NewReader(""))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	restrictedEngine.ServeHTTP(w, req)
	if passed := strings.TrimSpace(w.Body.String()) == "ok"; passed {
		t.Fatalf("受限会话不应访问 /server/*，却返回了业务响应: %s", w.Body.String())
	}

	// 管理员会话：应放行。
	adminEngine := build(func(c *gin.Context) {
		c.Set("base_path", "/")
		if err := session.SetLoginUser(c, admin); err != nil {
			t.Fatal(err)
		}
		c.Next()
	})

	w = httptest.NewRecorder()
	adminEngine.ServeHTTP(w, req)
	if passed := strings.TrimSpace(w.Body.String()) == "ok"; !passed {
		t.Fatalf("管理员会话应放行 /server/*，却返回: %s", w.Body.String())
	}
}
