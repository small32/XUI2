package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"

	"x-ui/web/session"
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
