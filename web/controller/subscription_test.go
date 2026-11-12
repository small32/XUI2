package controller

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"xui/database"
	"xui/database/model"
	"xui/web/service"
)

// 客户端拿 Content-Disposition 里的文件名当订阅显示名，这里固定为 XUI。
func TestSubscriptionNamedXUI(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("XUI_ROLE", "agent")
	if err := database.InitDB(filepath.Join(t.TempDir(), "sub.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.CloseDB)
	service.SetSubscriptionRules([]byte("[custom]\nruleset=🐟 漏网之鱼,[]FINAL\n"))

	in := model.Inbound{
		Port:           8443,
		Protocol:       model.Shadowsocks,
		Remark:         "A",
		Enable:         true,
		Settings:       `{"method":"aes-128-gcm","password":"pw"}`,
		StreamSettings: `{}`,
		Sniffing:       `{}`,
	}
	if err := database.GetDB().Create(&in).Error; err != nil {
		t.Fatal(err)
	}
	token, err := new(service.SubscriptionService).Sign(in.Id)
	if err != nil {
		t.Fatal(err)
	}

	engine := gin.New()
	RegisterSubscription(engine.Group("/"))
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/sub/"+token, nil))
	if w.Code != http.StatusOK {
		t.Fatalf("订阅请求失败: %d %s", w.Code, w.Body.String())
	}
	if cd := w.Header().Get("Content-Disposition"); !strings.Contains(cd, `filename="XUI"`) {
		t.Fatalf("订阅文件名不是 XUI: %q", cd)
	}
	if !strings.Contains(w.Body.String(), "MATCH,") {
		t.Fatalf("订阅内容不像 Clash 配置:\n%s", w.Body.String())
	}

	// 伪造令牌必须被拒绝
	w = httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/sub/1-deadbeef", nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("错误令牌没有被拒绝: %d", w.Code)
	}
}
