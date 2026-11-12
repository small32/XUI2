package controller

import (
	"fmt"
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

// 订阅显示名取入站列表里的用户名（remark），中文用户名走 filename* 编码。
func TestSubscriptionNamedAfterInboundUser(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("XUI_ROLE", "agent")
	if err := database.InitDB(filepath.Join(t.TempDir(), "sub.db")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.CloseDB)
	service.SetSubscriptionRules([]byte("[custom]\nruleset=🐟 漏网之鱼,[]FINAL\n"))

	for i, tc := range []struct {
		remark  string
		ascii   string
		encoded string
	}{
		{remark: "alice", ascii: "alice", encoded: "alice"},
		{remark: "测试用户", ascii: "XUI", encoded: "%E6%B5%8B%E8%AF%95%E7%94%A8%E6%88%B7"},
		{remark: "", ascii: "XUI", encoded: "XUI"},
	} {
		in := model.Inbound{
			Port:           8443 + i,
			Protocol:       model.Shadowsocks,
			Remark:         tc.remark,
			Tag:            fmt.Sprintf("sub-test-%d", i),
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
		cd := w.Header().Get("Content-Disposition")
		if !strings.Contains(cd, `filename="`+tc.ascii+`"`) {
			t.Fatalf("用户名 %q 的 ASCII 兜底不对: %q", tc.remark, cd)
		}
		if !strings.Contains(cd, "filename*=UTF-8''"+tc.encoded) {
			t.Fatalf("用户名 %q 的编码不对: %q", tc.remark, cd)
		}
		if !strings.Contains(w.Body.String(), "MATCH,") {
			t.Fatalf("订阅内容不像 Clash 配置:\n%s", w.Body.String())
		}
	}

	// 伪造令牌必须被拒绝
	engine := gin.New()
	RegisterSubscription(engine.Group("/"))
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/sub/1-deadbeef", nil))
	if w.Code != http.StatusForbidden {
		t.Fatalf("错误令牌没有被拒绝: %d", w.Code)
	}
}
