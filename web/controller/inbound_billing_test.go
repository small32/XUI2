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

	"x-ui/database"
	"x-ui/database/model"
)

// 入站表单的「按月计算」勾选必须真正落库：写不进去会让账号一直累计（客户月初用不了），
// 也回退不了（取消勾选是切到“流量用完即止”的唯一入口）。
func TestInboundMonthlyResetRoundTrip(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "x-ui.db")); err != nil {
		t.Fatal(err)
	}
	db := database.GetDB()
	in := &model.Inbound{
		UserId: 1,
		Port:   3101, Protocol: model.VLESS, Total: 1 << 20, Remark: "客户",
		Enable: true, Tag: "inbound-3101", Settings: "{}", StreamSettings: "{}", Sniffing: "{}",
	}
	if err := db.Create(in).Error; err != nil {
		t.Fatal(err)
	}

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(sessions.Sessions("session", cookie.NewStore([]byte("test-secret"))))
	engine.POST("/xui/inbound/update/:id", (&InboundController{}).updateInbound)

	// 表单提交的字段与页面 updateInbound 组装的一致。
	payload := func(monthly bool) string {
		return `{"up":0,"down":0,"total":1048576,"remark":"客户","enable":true,"expiryTime":0,` +
			`"monthlyReset":` + strconv.FormatBool(monthly) + `,"listen":"","port":3101,"protocol":"vless",` +
			`"settings":"{}","streamSettings":"{}","sniffing":"{}"}`
	}
	post := func(body string) string {
		t.Helper()
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/xui/inbound/update/"+strconv.Itoa(in.Id), strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		engine.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("状态码 %d: %s", w.Code, w.Body.String())
		}
		return w.Body.String()
	}

	// 勾选：写入后落库为真。
	if resp := post(payload(true)); !strings.Contains(resp, `"success":true`) {
		t.Fatalf("勾选按月计算失败: %s", resp)
	}
	if !inboundMonthlyReset(t, 3101) {
		t.Fatal("勾选后未落库为按月计算")
	}

	// 取消勾选：必须能改回累计计费。
	if resp := post(payload(false)); !strings.Contains(resp, `"success":true`) {
		t.Fatalf("取消按月计算失败: %s", resp)
	}
	if inboundMonthlyReset(t, 3101) {
		t.Fatal("取消勾选后仍是按月计算")
	}

	// 切来切去不能顺带改动别的字段。
	var stored model.Inbound
	if err := db.Where("port = ?", 3101).First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Total != 1<<20 || stored.Remark != "客户" || !stored.Enable {
		t.Fatalf("改动波及了其他字段: %+v", stored)
	}
	// 列表接口直接返回 model.Inbound，页面按这个字段名渲染勾选状态，锁住它防止前后端脱节。
	stored.MonthlyReset = true
	b, err := json.Marshal(&stored)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"monthlyReset":true`) {
		t.Fatalf("入站 JSON 未按页面约定的字段名输出按月标记: %s", b)
	}
}

func inboundMonthlyReset(t *testing.T, port int) bool {
	t.Helper()
	var in model.Inbound
	if err := database.GetDB().Where("port = ?", port).First(&in).Error; err != nil {
		t.Fatal(err)
	}
	return in.MonthlyReset
}
