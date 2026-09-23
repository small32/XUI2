package controller

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"x-ui/web/session"
)

type XUIController struct {
	BaseController

	inboundController *InboundController
	settingController *SettingController
}

func NewXUIController(g *gin.RouterGroup) *XUIController {
	a := &XUIController{}
	a.initRouter(g)
	return a
}

func (a *XUIController) initRouter(g *gin.RouterGroup) {
	g = g.Group("/xui")
	g.Use(a.checkLogin)
	g.Use(a.checkRestricted)

	g.GET("/", a.index)
	g.GET("/inbounds", a.inbounds)
	g.GET("/setting", a.setting)

	a.inboundController = NewInboundController(g)
	a.settingController = NewSettingController(g)
	NewServerManagementController(g)
}

// checkRestricted 受限登录（入站端口号登录）仅可访问入站列表页、入站列表数据、其订阅数据，
// 以及流量汇总页、本入站的流量汇总数据与本入站的月度留档，其余请求一律拦截，
// 避免仅靠前端隐藏菜单造成的越权。
func (a *XUIController) checkRestricted(c *gin.Context) {
	if !session.IsRestricted(c) {
		c.Next()
		return
	}
	basePath := c.GetString("base_path")
	rel := strings.TrimPrefix(c.Request.URL.Path, basePath)
	switch rel {
	case "", "xui", "xui/":
		c.Redirect(http.StatusTemporaryRedirect, basePath+"xui/inbounds")
		c.Abort()
		return
	case "xui/inbounds", "xui/inbound/list", "xui/inbound/subscription",
		"xui/traffic-summary", "xui/traffic-summary/list", "xui/traffic-summary/snapshots":
		c.Next()
		return
	}
	if strings.HasPrefix(rel, "xui/traffic-summary/nodes/") {
		c.Next()
		return
	}
	if isAjax(c) {
		pureJsonMsg(c, false, "登录账号无权访问该功能")
	} else {
		c.Redirect(http.StatusTemporaryRedirect, basePath+"xui/inbounds")
	}
	c.Abort()
}

func (a *XUIController) index(c *gin.Context) {
	html(c, "index.html", "系统状态", nil)
}

func (a *XUIController) inbounds(c *gin.Context) {
	html(c, "inbounds.html", "入站列表", nil)
}

func (a *XUIController) setting(c *gin.Context) {
	html(c, "setting.html", "设置", nil)
}
