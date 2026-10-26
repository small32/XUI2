package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
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

// 面板公告接口与客户端公告接口的端到端行为：
// 管理员发布后，面板登录用户能读到，SClash 用客户端令牌能读到，停用后两边都不再返回。
func TestAnnouncementPanelAndClientAPI(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("XUI_ROLE", "manager")
	if err := database.InitDB(filepath.Join(t.TempDir(), "announcement-api.db")); err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := database.GetDB().DB()
	t.Cleanup(func() { sqlDB.Close() })

	in := model.Inbound{Port: 18890, Enable: true, Protocol: model.Shadowsocks, Settings: `{"method":"aes-128-gcm","password":"secret"}`, StreamSettings: `{}`, Sniffing: `{}`, Tag: "inbound-18890", Total: 1000}
	if err := database.GetDB().Create(&in).Error; err != nil {
		t.Fatal(err)
	}

	engine := gin.New()
	engine.Use(func(c *gin.Context) { c.Set("base_path", "/panel/"); c.Next() })
	panel := engine.Group("/panel")
	RegisterClientAPI(panel)
	NewAnnouncementController(panel)

	call := func(method, path, body, token string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, "https://manager.example"+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		engine.ServeHTTP(w, r)
		return w
	}

	// 没有公告时，面板与客户端都应拿到空列表，客户端还要有稳定版本号。
	w := call(http.MethodGet, "/panel/announcement/active", "", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"success":true`) {
		t.Fatalf("panel active: %d %s", w.Code, w.Body.String())
	}

	if w := call(http.MethodGet, "/panel/api/client/v1/announcements", "", ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("客户端公告接口未登录应返回 401，实际 %d", w.Code)
	}

	w = call(http.MethodPost, "/panel/api/client/v1/auth/login", `{"port":"18890","password":"secret"}`, "")
	var tokens service.ClientTokens
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &tokens) != nil {
		t.Fatalf("客户端登录失败: %d %s", w.Code, w.Body.String())
	}

	// 停用的公告对两边都不可见。
	if _, err := new(service.AnnouncementService).Save(&service.AnnouncementInput{
		Title: "停用公告", Content: "不应该出现", Audience: service.AnnouncementAudienceAll, Enabled: false,
	}); err != nil {
		t.Fatal(err)
	}
	w = call(http.MethodGet, "/panel/api/client/v1/announcements", "", tokens.AccessToken)
	if w.Code != http.StatusOK || strings.Contains(w.Body.String(), "停用公告") {
		t.Fatalf("停用公告不应下发: %d %s", w.Code, w.Body.String())
	}

	// 启用后的公告对面板与客户端都可见。
	saved, err := new(service.AnnouncementService).Save(&service.AnnouncementInput{
		Title: "客户端更新", Content: "请升级到 1.2.1", Level: service.AnnouncementLevelImportant,
		Audience: service.AnnouncementAudienceAll, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	w = call(http.MethodGet, "/panel/announcement/active", "", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "客户端更新") {
		t.Fatalf("面板公告未返回: %d %s", w.Code, w.Body.String())
	}

	w = call(http.MethodGet, "/panel/api/client/v1/announcements", "", tokens.AccessToken)
	if w.Code != http.StatusOK {
		t.Fatalf("客户端公告接口: %d %s", w.Code, w.Body.String())
	}
	var feed service.ClientAnnouncementFeed
	if err := json.Unmarshal(w.Body.Bytes(), &feed); err != nil {
		t.Fatalf("客户端公告响应不是合法 JSON: %s", w.Body.String())
	}
	if len(feed.Announcements) != 1 || feed.Announcements[0].Title != "客户端更新" || feed.Announcements[0].Level != service.AnnouncementLevelImportant {
		t.Fatalf("客户端公告内容不正确: %+v", feed.Announcements)
	}
	if feed.Version == "" || w.Header().Get("ETag") == "" {
		t.Fatal("客户端公告应带版本号与 ETag")
	}
	if saved.Id == 0 {
		t.Fatal("保存后的公告应带编号")
	}
}

// 管理接口必须登录后才可用，未登录不得读到或修改公告。
func TestAnnouncementAdminRequiresLogin(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("XUI_ROLE", "manager")
	if err := database.InitDB(filepath.Join(t.TempDir(), "announcement-admin.db")); err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := database.GetDB().DB()
	t.Cleanup(func() { sqlDB.Close() })

	engine := gin.New()
	engine.Use(func(c *gin.Context) { c.Set("base_path", "/panel/"); c.Next() })
	// 管理接口依赖会话中间件判定登录态，这里装上真实的 cookie store，
	// 未登录请求应被 checkAdminLogin 拦下。
	engine.Use(sessions.Sessions("session", cookie.NewStore([]byte("announcement-test-secret"), []byte("announcement-test-encryption-key"))))
	NewAnnouncementController(engine.Group("/panel"))

	for _, path := range []string{"/panel/announcement/list", "/panel/announcement/save", "/panel/announcement/delete"} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "https://manager.example"+path, strings.NewReader(`{"title":"x","content":"y"}`))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Requested-With", "XMLHttpRequest")
		engine.ServeHTTP(w, r)
		if w.Code == http.StatusOK && strings.Contains(w.Body.String(), `"success":true`) {
			t.Fatalf("%s 未登录不应成功: %s", path, w.Body.String())
		}
	}

	rows, err := new(service.AnnouncementService).List()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatal("未登录请求不应写入公告")
	}
}

// 历史公告、面板已读、客户端已读上报、管理员阅读记录的接口行为。
func TestAnnouncementHistoryAndReadAPI(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("XUI_ROLE", "manager")
	if err := database.InitDB(filepath.Join(t.TempDir(), "announcement-history.db")); err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := database.GetDB().DB()
	t.Cleanup(func() { sqlDB.Close() })

	in := model.Inbound{Port: 18891, Enable: true, Protocol: model.Shadowsocks, Settings: `{"method":"aes-128-gcm","password":"secret"}`, StreamSettings: `{}`, Sniffing: `{}`, Tag: "inbound-18891", Total: 1000}
	if err := database.GetDB().Create(&in).Error; err != nil {
		t.Fatal(err)
	}

	svc := new(service.AnnouncementService)
	panelRow, err := svc.Save(&service.AnnouncementInput{Title: "面板公告", Content: "内容", Audience: service.AnnouncementAudiencePanel, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	clientRow, err := svc.Save(&service.AnnouncementInput{Title: "客户端公告", Content: "内容", Audience: service.AnnouncementAudienceClient, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}

	engine := gin.New()
	engine.Use(func(c *gin.Context) { c.Set("base_path", "/panel/"); c.Next() })
	engine.Use(sessions.Sessions("session", cookie.NewStore([]byte("announcement-history-secret"), []byte("announcement-history-encryption"))))
	panel := engine.Group("/panel")
	RegisterClientAPI(panel)
	NewAnnouncementController(panel)

	call := func(method, path, body, token string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, "https://manager.example"+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Requested-With", "XMLHttpRequest")
		if token != "" {
			r.Header.Set("Authorization", "Bearer "+token)
		}
		engine.ServeHTTP(w, r)
		return w
	}

	// 历史公告对面板登录用户开放（这里只验证路由与数据形态，登录态由会话中间件保证）。
	w := call(http.MethodGet, "/panel/announcement/history?page=1&size=20", "", "")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"success":true`) || !strings.Contains(w.Body.String(), "面板公告") {
		t.Fatalf("历史公告接口: %d %s", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), "客户端公告") {
		t.Fatalf("历史公告不应包含仅客户端公告: %s", w.Body.String())
	}

	// 未登录不得写已读。
	w = call(http.MethodPost, "/panel/announcement/read", `{"ids":[1]}`, "")
	if w.Code == http.StatusOK && strings.Contains(w.Body.String(), `"success":true`) {
		t.Fatalf("未登录不应能记录已读: %s", w.Body.String())
	}

	// 阅读记录只有管理员能看。
	w = call(http.MethodPost, "/panel/announcement/receipts", `{"id":1}`, "")
	if w.Code == http.StatusOK && strings.Contains(w.Body.String(), `"success":true`) {
		t.Fatalf("未登录不应能看阅读记录: %s", w.Body.String())
	}

	// 客户端上报已读。
	w = call(http.MethodPost, "/panel/api/client/v1/auth/login", `{"port":"18891","password":"secret"}`, "")
	var tokens service.ClientTokens
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &tokens) != nil {
		t.Fatalf("客户端登录失败: %d %s", w.Code, w.Body.String())
	}
	if w = call(http.MethodPost, "/panel/api/client/v1/announcements/read", `{"ids":[1]}`, ""); w.Code != http.StatusUnauthorized {
		t.Fatalf("客户端上报已读必须登录，实际 %d", w.Code)
	}
	if w = call(http.MethodPost, "/panel/api/client/v1/announcements/read", `{"ids":[`+strconv.Itoa(clientRow.Id)+`]}`, tokens.AccessToken); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"success":true`) {
		t.Fatalf("客户端上报已读: %d %s", w.Code, w.Body.String())
	}

	views, err := svc.Receipts(clientRow.Id)
	if err != nil || len(views) != 1 || views[0].Label != "SClash 18891" {
		t.Fatalf("客户端已读记录不正确: %+v %v", views, err)
	}
	// 客户端不能把仅面板公告记成已读。
	if w = call(http.MethodPost, "/panel/api/client/v1/announcements/read", `{"ids":[`+strconv.Itoa(panelRow.Id)+`]}`, tokens.AccessToken); w.Code != http.StatusOK {
		t.Fatalf("上报仅面板公告应被忽略而不是报错: %d", w.Code)
	}
	if views, err = svc.Receipts(panelRow.Id); err != nil || len(views) != 0 {
		t.Fatalf("仅面板公告不应有客户端记录: %+v %v", views, err)
	}
}

// 同一浏览器、先后以管理员和端口账号登录：管理员确认过的公告不应"替"端口账号读掉。
// 这是会话切换场景的接口层防线——已读必须挂在账号上，而不是挂在浏览器上。
func TestAnnouncementReadIsPerLoginAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	t.Setenv("XUI_ROLE", "manager")
	if err := database.InitDB(filepath.Join(t.TempDir(), "announcement-account.db")); err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := database.GetDB().DB()
	t.Cleanup(func() { sqlDB.Close() })

	in := model.Inbound{Port: 18892, Enable: true, Protocol: model.Shadowsocks, Settings: `{"method":"aes-128-gcm","password":"secret"}`, StreamSettings: `{}`, Sniffing: `{}`, Tag: "inbound-18892", Total: 1000}
	if err := database.GetDB().Create(&in).Error; err != nil {
		t.Fatal(err)
	}
	admin := model.User{Username: "admin", Password: "hash"}
	if err := database.GetDB().Create(&admin).Error; err != nil {
		t.Fatal(err)
	}

	svc := new(service.AnnouncementService)
	row, err := svc.Save(&service.AnnouncementInput{Title: "系统维护", Content: "周六维护",
		Audience: service.AnnouncementAudiencePanel, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}

	engine := gin.New()
	engine.Use(func(c *gin.Context) { c.Set("base_path", "/panel/"); c.Next() })
	// securecookie 的加密 key 必须是 16/24/32 字节，这里取满 32。
	engine.Use(sessions.Sessions("session", cookie.NewStore(
		[]byte("announcement-account-secret!!"),
		[]byte("0123456789abcdef0123456789abcdef"),
	)))
	// 测试替身：按请求头决定这次调用代表哪个账号登录，会话本身走真实的读写路径。
	engine.Use(func(c *gin.Context) {
		switch c.GetHeader("X-Test-Actor") {
		case "admin":
			if err := session.SetLoginUser(c, &admin); err != nil {
				t.Fatal(err)
			}
		case "port":
			if err := session.SetRestrictedLogin(c, in.Id, "secret"); err != nil {
				t.Fatal(err)
			}
		}
		c.Next()
	})
	NewAnnouncementController(engine.Group("/panel"))

	call := func(method, path, body, actor string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, "https://manager.example"+path, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Requested-With", "XMLHttpRequest")
		if actor != "" {
			r.Header.Set("X-Test-Actor", actor)
		}
		engine.ServeHTTP(w, r)
		return w
	}

	activeRead := func(actor string) bool {
		t.Helper()
		w := call(http.MethodGet, "/panel/announcement/active", "", actor)
		if w.Code != http.StatusOK {
			t.Fatalf("active 接口: %d %s", w.Code, w.Body.String())
		}
		var payload struct {
			Obj []model.Announcement `json:"obj"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
			t.Fatalf("active 响应解析失败: %s", w.Body.String())
		}
		if len(payload.Obj) != 1 {
			t.Fatalf("应返回 1 条公告，实际 %d", len(payload.Obj))
		}
		return payload.Obj[0].Read
	}

	if activeRead("admin") || activeRead("port") {
		t.Fatal("两个账号都应先看到未读公告")
	}
	w := call(http.MethodPost, "/panel/announcement/read", `{"ids":[`+strconv.Itoa(row.Id)+`]}`, "admin")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"success":true`) {
		t.Fatalf("管理员上报已读: %d %s", w.Code, w.Body.String())
	}
	if !activeRead("admin") {
		t.Fatal("管理员读过后应显示已读")
	}
	if activeRead("port") {
		t.Fatal("端口账号不应被管理员的阅读记录影响")
	}
	w = call(http.MethodPost, "/panel/announcement/read", `{"ids":[`+strconv.Itoa(row.Id)+`]}`, "port")
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"success":true`) {
		t.Fatalf("端口账号上报已读: %d %s", w.Code, w.Body.String())
	}
	if !activeRead("port") {
		t.Fatal("端口账号自己读过后才应显示已读")
	}
}
