package controller

import (
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

func TestAgentLocalInboundEditing(t *testing.T) {
	t.Setenv("XUI_ROLE", "agent")
	if err := database.InitDB(filepath.Join(t.TempDir(), "xui.db")); err != nil {
		t.Fatal(err)
	}
	in := model.Inbound{UserId: 1, ManagerAccountID: 42, ManagerRevision: "accepted-revision", Port: 3102, Protocol: model.VLESS, Remark: "客户", Enable: true, Up: 123, Down: 456, Tag: "inbound-3102", Settings: "{}", StreamSettings: "{}", Sniffing: "{}"}
	if err := database.GetDB().Create(&in).Error; err != nil {
		t.Fatal(err)
	}
	ctrl := &InboundController{}
	ctrl.xrayService.IsNeedRestartAndSetFalse()
	t.Cleanup(func() { ctrl.xrayService.IsNeedRestartAndSetFalse() })
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/update/:id", ctrl.updateInbound)
	post := func() string {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/update/"+strconv.Itoa(in.Id), strings.NewReader(`{"id":999,"remark":"客户","port":3102,"protocol":"vless","enable":false,"settings":"{\"clients\":[]}","streamSettings":"{}","sniffing":"{}","managerRevision":"forged","managerAccountId":999,"up":0,"down":0}`))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)
		return w.Body.String()
	}
	if response := post(); !strings.Contains(response, `"success":false`) {
		t.Fatalf("default must deny local editing: %s", response)
	}
	settings := service.SettingService{}
	all, err := settings.GetAllSetting()
	if err != nil {
		t.Fatal(err)
	}
	all.LocalSettingEnable = true
	if err := settings.UpdateAllSetting(all); err != nil {
		t.Fatal(err)
	}
	if response := post(); !strings.Contains(response, `"success":true`) {
		t.Fatalf("enabled editing failed: %s", response)
	}
	var stored model.Inbound
	if err := database.GetDB().First(&stored, in.Id).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Enable || stored.Settings != `{"clients":[]}` {
		t.Fatalf("local edits not saved: %+v", stored)
	}
	if stored.ManagerAccountID != 42 || stored.ManagerRevision != "accepted-revision" || stored.Up != 123 || stored.Down != 456 || stored.UserId != 1 {
		t.Fatalf("local edit changed managed identity or counters: %+v", stored)
	}
	if !ctrl.xrayService.IsNeedRestartAndSetFalse() {
		t.Fatal("local edit did not schedule xray reload")
	}
	var count int64
	database.GetDB().Model(&model.SyncTask{}).Count(&count)
	if count != 0 {
		t.Fatal("agent edit queued manager synchronization")
	}
	all.LocalSettingEnable = false
	if err := settings.UpdateAllSetting(all); err != nil {
		t.Fatal(err)
	}
	if response := post(); !strings.Contains(response, `"success":false`) {
		t.Fatalf("disabled editing still allowed: %s", response)
	}
}

func TestAgentLocalInboundCreation(t *testing.T) {
	t.Setenv("XUI_ROLE", "agent")
	if err := database.InitDB(filepath.Join(t.TempDir(), "xui.db")); err != nil {
		t.Fatal(err)
	}
	ctrl := &InboundController{}
	ctrl.xrayService.IsNeedRestartAndSetFalse()
	t.Cleanup(func() { ctrl.xrayService.IsNeedRestartAndSetFalse() })
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(sessions.Sessions("session", cookie.NewStore([]byte("test-secret"))))
	router.Use(func(c *gin.Context) {
		if err := session.SetLoginUser(c, &model.User{Id: 1, Username: "admin", Password: "test-hash"}); err != nil {
			t.Fatal(err)
		}
	})
	router.POST("/add", ctrl.addInbound)
	post := func() string {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/add", strings.NewReader(`{"id":999,"remark":"本地用户","port":3201,"protocol":"vless","settings":"{}","streamSettings":"{}","sniffing":"{}","managerAccountId":42,"managerRevision":"forged","up":123,"down":456}`))
		req.Header.Set("Content-Type", "application/json")
		router.ServeHTTP(w, req)
		return w.Body.String()
	}
	if response := post(); !strings.Contains(response, `"success":false`) {
		t.Fatalf("default allowed creation: %s", response)
	}
	settings := service.SettingService{}
	all, err := settings.GetAllSetting()
	if err != nil {
		t.Fatal(err)
	}
	all.LocalSettingEnable = true
	if err := settings.UpdateAllSetting(all); err != nil {
		t.Fatal(err)
	}
	if response := post(); !strings.Contains(response, `"success":true`) {
		t.Fatalf("local creation failed: %s", response)
	}
	var stored model.Inbound
	if err := database.GetDB().Where("port = ?", 3201).First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Id == 999 || stored.ManagerAccountID != 0 || stored.ManagerRevision != "" || stored.Up != 0 || stored.Down != 0 || stored.UserId != 1 || !stored.Enable {
		t.Fatalf("incorrect local inbound: %+v", stored)
	}
	if !ctrl.xrayService.IsNeedRestartAndSetFalse() {
		t.Fatal("creation did not request xray reload")
	}
	if response := post(); !strings.Contains(response, `"success":false`) {
		t.Fatalf("duplicate port allowed: %s", response)
	}
	all.LocalSettingEnable = false
	if err := settings.UpdateAllSetting(all); err != nil {
		t.Fatal(err)
	}
	if response := post(); !strings.Contains(response, "请先在面板设置中启用本地设置") {
		t.Fatalf("disabled creation not rejected: %s", response)
	}
	var count int64
	if err := database.GetDB().Model(&model.SyncTask{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("local creation queued manager synchronization")
	}
}
