package web

import (
	"bytes"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func renderTpl(t *testing.T, name string, data gin.H) string {
	t.Helper()
	gin.SetMode(gin.TestMode)
	server := new(Server)
	engine := gin.New()
	if err := server.initI18n(engine); err != nil {
		t.Fatal(err)
	}
	tpl, err := server.getHtmlTemplate(engine.FuncMap)
	if err != nil {
		t.Fatal(err)
	}
	if tpl.Lookup(name) == nil {
		t.Fatalf("模板表里找不到 %s", name)
	}
	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, name, data); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

// setting.html 在 agent 角色下应渲染"对外主机名"设置项与"被控端连接信息"区；
// 在 manager 角色下不应出现 agent 专属区块。
func TestSettingAgentConnectionInfo(t *testing.T) {
	base := gin.H{"title": "设置", "request_uri": "/xui/setting", "base_path": "/", "cur_ver": "0.0.0"}

	agent := renderTpl(t, "setting.html", mergeData(base, gin.H{"role": "agent"}))
	for _, want := range []string{"对外主机名", "被控端连接信息", "externalHost", "loadConnInfo", "刷新指纹", "refreshFingerprint"} {
		if !strings.Contains(agent, want) {
			t.Fatalf("agent 设置页应包含 %q", want)
		}
	}

	manager := renderTpl(t, "setting.html", mergeData(base, gin.H{"role": "manager"}))
	for _, forbid := range []string{"被控端连接信息", "externalHost"} {
		if strings.Contains(manager, forbid) {
			t.Fatalf("manager 设置页不应包含 %q", forbid)
		}
	}
}

// server.html 管理端被控端管理页应包含聚合列与新接口。
func TestServerNodeAggregateColumns(t *testing.T) {
	body := renderTpl(t, "server.html", gin.H{"title": "被控端管理", "request_uri": "/xui/server", "base_path": "/", "cur_ver": "0.0.0", "role": "manager"})
	for _, want := range []string{"入站数", "总流量", "启用数", "inboundCount", "usedText", "enabledCount", "/xui/server/nodes/list", ":min=\"5\""} {
		if !strings.Contains(body, want) {
			t.Fatalf("server.html 应包含 %q", want)
		}
	}
}

// setting.html 管理端应有通知公告管理区，agent 角色不应出现该入口。
func TestSettingAnnouncementTab(t *testing.T) {
	manager := renderTpl(t, "setting.html", gin.H{"title": "设置", "request_uri": "/xui/setting", "base_path": "/", "cur_ver": "0.0.0", "role": "manager"})
	for _, want := range []string{"通知公告", "loadAnnouncements", "saveAnnouncement", "announcementModalVisible", "announcementColumns", "推送范围"} {
		if !strings.Contains(manager, want) {
			t.Fatalf("manager 设置页应包含 %q", want)
		}
	}
	agent := renderTpl(t, "setting.html", gin.H{"title": "设置", "request_uri": "/xui/setting", "base_path": "/", "cur_ver": "0.0.0", "role": "agent"})
	// 侧栏的「通知公告」入口对所有角色都有，这里要断言的是管理区本身没有渲染。
	for _, forbid := range []string{"新建公告", "全部状态", "搜索标题"} {
		if strings.Contains(agent, forbid) {
			t.Fatalf("agent 设置页不应包含通知公告管理区: %q", forbid)
		}
	}
}

// 历史公告页要能渲染，并带上侧栏入口与历史接口。
func TestAnnouncementHistoryTemplate(t *testing.T) {
	body := renderTpl(t, "announcements.html", gin.H{"title": "通知公告", "request_uri": "/xui/announcements", "base_path": "/", "cur_ver": "0.0.0", "role": "manager"})
	for _, want := range []string{"通知公告", "xui/announcement/history", "__xuiAnnouncementPage", "XuiAnnouncement"} {
		if !strings.Contains(body, want) {
			t.Fatalf("历史公告页应包含 %q", want)
		}
	}
	// 端口账号（受限登录）也应有入口。
	restricted := renderTpl(t, "announcements.html", gin.H{"title": "通知公告", "request_uri": "/xui/announcements", "base_path": "/", "cur_ver": "0.0.0", "role": "manager", "restricted": true})
	if !strings.Contains(restricted, "xui/announcements") {
		t.Fatal("受限登录的菜单里也应有通知公告入口")
	}
}

func mergeData(a gin.H, b gin.H) gin.H {
	for k, v := range b {
		a[k] = v
	}
	return a
}
