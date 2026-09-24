package web

import (
	"bytes"
	"io/fs"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// 流量汇总页是压缩成单行的模板，改错一个字符整页就会渲染失败，这里锁住它。
// 必须用与服务一致的函数表（initI18n 注册的 i18n），否则解析会中途失败。
func TestTrafficSummaryTemplate(t *testing.T) {
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
	if tpl.Lookup("traffic_summary.html") == nil {
		t.Fatal("模板表里找不到 traffic_summary.html")
	}
	var buf bytes.Buffer
	// 页面里的 {{ .restricted }} 由控制器注入，测试也得带上这个键，
	// 否则模板会因取不到字段而执行失败。
	data := gin.H{
		"restricted":  false,
		"title":       "流量汇总",
		"request_uri": "/xui/traffic-summary",
		"base_path":   "/",
		"cur_ver":     "0.0.0",
	}
	if err = tpl.ExecuteTemplate(&buf, "traffic_summary.html", data); err != nil {
		t.Fatal(err)
	}
	body := buf.String()
	// 单位换算在后端完成，页面只渲染已换算好的文本列。
	// 汇总表默认只展示总流量；被控端流量不直接列示，需点"节点明细"查看。
	for _, column := range []string{
		`data-index="totalText"`,
		`data-index="limitText"`,
	} {
		if !strings.Contains(body, column) {
			t.Fatalf("页面缺少列 %s", column)
		}
	}
	if strings.Contains(body, `data-index="remoteText"`) {
		t.Fatalf("汇总表不应再直接展示被控端流量列 remoteText，需经节点明细查看")
	}
	for _, stale := range []string{`data-index="local"`, `data-index="remote"`, `data-index="total"`, `data-index="limit"`} {
		if strings.Contains(body, stale) {
			t.Fatalf("页面仍在绑定未换算的字节列 %s", stale)
		}
	}
	// 快照入口：状态列旁边一列"查看"，弹窗里的月份列由后端排好序，
	// 每行的"查看"按账号拉取，别退化成一次拉全量再由前端过滤。
	for _, want := range []string{
		`title="快照"`,
		`openSnapshots(row)`,
		`/xui/traffic-summary/snapshots`,
		`data-index="usedText"`,
		`row-key="yyyymm"`,
		`inboundId: row.inboundId`,
		`openNodes(row)`,
		`/xui/traffic-summary/nodes/`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("页面缺少快照入口 %s", want)
		}
	}
	// 三态筛选：状态由后端判定（enabled／disabled／overlimit），
	// 页面只按 status 做并集过滤，避免前后端各算一套口径。
	for _, want := range []string{
		`a-checkbox-group v-if="!restricted" v-model="filters"`,
		`value="enabled"`,
		`value="disabled"`,
		`value="overlimit"`,
		`visibleRows`,
		`filters.indexOf(row.status)`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("页面缺少三态筛选 %s", want)
		}
	}
	// 计费方式取自后端的 monthlyReset：按月／累计一眼可辨，
	// 没勾“按月计算”的账号本来就没有月度留档，空态得说明原因而不是显示成丢数据。
	for _, want := range []string{
		`title="计费方式"`,
		`row.monthlyReset`,
		`snapshotModal.emptyText`,
		`:locale="{ emptyText: snapshotModal.emptyText }"`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("页面缺少计费方式或快照空态 %s", want)
		}
	}
}

// 两个列表页的三态筛选都必须挂在 !restricted 上：端口号账号登录时不显示筛选器。
// 这里直接读嵌入文件而不执行模板，因为 inbounds.html 还依赖若干弹窗子模板的上下文。
func TestThreeStateFilterOnlyForAdmin(t *testing.T) {
	for _, name := range []string{"html/xui/traffic_summary.html", "html/xui/inbounds.html"} {
		b, err := fs.ReadFile(htmlFS, name)
		if err != nil {
			t.Fatalf("读取 %s 失败: %v", name, err)
		}
		body := string(b)
		for _, want := range []string{
			`a-checkbox-group v-if="!restricted" v-model="filters"`,
			`value="enabled">已启用`,
			`value="disabled">已停用`,
			`value="overlimit">已超限`,
		} {
			if !strings.Contains(body, want) {
				t.Errorf("%s 缺少三态筛选 %s", name, want)
			}
		}
	}
}
