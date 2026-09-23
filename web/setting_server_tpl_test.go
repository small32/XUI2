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
	for _, want := range []string{"对外主机名", "被控端连接信息", "externalHost", "loadConnInfo"} {
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

func mergeData(a gin.H, b gin.H) gin.H {
	for k, v := range b {
		a[k] = v
	}
	return a
}
