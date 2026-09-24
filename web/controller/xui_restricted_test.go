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

	"xui/database"
	"xui/database/model"
	"xui/web/session"
)

// 受限登录要能打开流量汇总页里的快照入口，同时其余路径仍被拦截。
// 白名单与实际路由分处两个文件，改路由时容易忘掉这一处，所以在这里锁住。
func TestRestrictedAccessAllowsTrafficSnapshots(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(sessions.Sessions("session", cookie.NewStore([]byte("test-secret"))))
	engine.Use(func(c *gin.Context) {
		// base_path 由 web.go 从面板设置读出，恒以 "/" 开头、以 "/" 结尾（默认 "/"）。
		// 白名单比对的是去掉前缀后的相对路径，这里必须与真实值一致。
		c.Set("base_path", "/")
		if err := session.SetLoginInboundId(c, 7); err != nil {
			t.Fatal(err)
		}
		c.Next()
	})
	engine.Use((&XUIController{}).checkRestricted)

	paths := []string{"/xui/traffic-summary/snapshots", "/xui/traffic-summary/list", "/xui/setting/all"}
	for _, path := range paths {
		engine.POST(path, func(c *gin.Context) { c.String(http.StatusOK, "ok") })
	}

	cases := []struct {
		path       string
		wantPassed bool
	}{
		{"/xui/traffic-summary/snapshots", true},
		{"/xui/traffic-summary/list", true},
		{"/xui/setting/all", false},
	}
	for _, cs := range cases {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, cs.path, strings.NewReader(""))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("X-Requested-With", "XMLHttpRequest")
		engine.ServeHTTP(w, req)
		if passed := strings.TrimSpace(w.Body.String()) == "ok"; passed != cs.wantPassed {
			t.Fatalf("%s 放行=%v，期望 %v：%s", cs.path, passed, cs.wantPassed, w.Body.String())
		}
	}
}

func TestRestrictedTrafficSessionExpiresAfterPasswordChange(t *testing.T) {
	gin.SetMode(gin.TestMode)
	if err := database.InitDB(filepath.Join(t.TempDir(), "sessions.db")); err != nil {
		t.Fatal(err)
	}
	in := model.Inbound{Port: 43339, Protocol: model.Trojan, Settings: `{"clients":[{"password":"old-secret"}]}`, StreamSettings: `{}`, Sniffing: `{}`, Tag: "inbound-43339"}
	if err := database.GetDB().Create(&in).Error; err != nil {
		t.Fatal(err)
	}
	engine := gin.New()
	engine.Use(sessions.Sessions("session", cookie.NewStore([]byte("test-secret"))))
	engine.Use(func(c *gin.Context) { c.Set("base_path", "/"); c.Next() })
	engine.GET("/test-login", func(c *gin.Context) {
		if err := session.SetRestrictedLogin(c, in.Id, "old-secret"); err != nil {
			t.Fatal(err)
		}
		c.String(200, "ok")
	})
	g := engine.Group("/xui")
	g.Use((&BaseController{}).checkLogin, (&XUIController{}).checkRestricted)
	g.POST("/traffic-summary/list", func(c *gin.Context) { c.String(200, "allowed") })
	login := httptest.NewRecorder()
	engine.ServeHTTP(login, httptest.NewRequest(http.MethodGet, "/test-login", nil))
	cookies := login.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("restricted session cookie missing")
	}
	request := func() string {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/xui/traffic-summary/list", nil)
		r.AddCookie(cookies[0])
		r.Header.Set("X-Requested-With", "XMLHttpRequest")
		engine.ServeHTTP(w, r)
		return w.Body.String()
	}
	if got := request(); got != "allowed" {
		t.Fatalf("valid restricted session was blocked: %s", got)
	}
	if err := database.GetDB().Model(&in).Update("settings", `{"clients":[{"password":"new-secret"}]}`).Error; err != nil {
		t.Fatal(err)
	}
	if got := request(); got == "allowed" {
		t.Fatal("old session still accessed traffic after password change")
	}
}
