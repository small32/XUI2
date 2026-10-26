package controller

import (
	"strconv"

	"github.com/gin-contrib/sessions"
	"github.com/gin-gonic/gin"
	"xui/web/service"
	"xui/web/session"
)

// AnnouncementController 提供面板侧的通知公告能力：
// 登录用户读取当前生效公告，管理员维护公告内容。
type AnnouncementController struct {
	BaseController
	service service.AnnouncementService
}

func NewAnnouncementController(g *gin.RouterGroup) *AnnouncementController {
	a := &AnnouncementController{}
	a.initRouter(g)
	return a
}

func (a *AnnouncementController) initRouter(g *gin.RouterGroup) {
	g = g.Group("/announcement")
	// 面板用户（含端口账号受限登录）读取当前生效的公告，用于登录后弹窗。
	g.GET("/active", a.active)
	// 面板用户翻看历史公告与上报已读。
	g.GET("/history", a.history)
	g.POST("/read", a.read)
	// 管理操作仅限管理员，受限登录一律拦截。
	admin := g.Group("")
	admin.Use(a.checkAdminLogin)
	admin.POST("/list", a.list)
	admin.POST("/save", a.save)
	admin.POST("/delete", a.delete)
	admin.POST("/receipts", a.receipts)
}

// panelActor 返回当前面板登录者的阅读记录标识：
// 管理员按用户编号，端口账号按端口号，两者互不覆盖。
// 端口账号查不到入站时返回空（放弃记录），绝不退回用户编号——否则会和真实管理员撞号。
// 会话中间件缺失时同样返回空：宁可全部按未读提醒，也不能让公告接口 panic。
func panelActor(c *gin.Context) string {
	if _, ok := c.Get(sessions.DefaultKey); !ok {
		return ""
	}
	if user := session.GetLoginUser(c); user != nil {
		return service.PanelUserActor(user.Id)
	}
	if session.IsRestricted(c) {
		inboundId := session.GetLoginInboundId(c)
		if inboundId > 0 {
			if in, err := new(service.InboundService).GetInbound(inboundId); err == nil && in != nil {
				return service.PanelPortActor(in.Port)
			}
		}
	}
	return ""
}

func (a *AnnouncementController) history(c *gin.Context) {
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("size", "20"))
	value, err := a.service.History(page, size, panelActor(c))
	jsonObj(c, value, err)
}

func (a *AnnouncementController) read(c *gin.Context) {
	var input struct {
		Ids []int `json:"ids"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		pureJsonMsg(c, false, "参数格式错误")
		return
	}
	actor := panelActor(c)
	if actor == "" {
		// 走到这里说明会话有效但账号已无法解析（例如端口账号对应的入站被删）。
		pureJsonMsg(c, false, "无法识别当前登录账号")
		return
	}
	if err := a.service.MarkRead(actor, service.AnnouncementAudiencePanel, input.Ids); err != nil {
		pureJsonMsg(c, false, "记录已读失败: "+err.Error())
		return
	}
	pureJsonMsg(c, true, "")
}

func (a *AnnouncementController) receipts(c *gin.Context) {
	var input struct {
		Id int `json:"id"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		jsonMsg(c, "获取阅读记录", err)
		return
	}
	rows, err := a.service.Receipts(input.Id)
	jsonObj(c, rows, err)
}

// active 返回当前账号可见且尚未确认的公告，未读与否由服务端按阅读记录判定，
// 不再依赖浏览器本地存储——否则同一台机器上先登录过的账号会把公告"替别人读掉"。
func (a *AnnouncementController) active(c *gin.Context) {
	rows, err := a.service.Active(service.AnnouncementAudiencePanel, panelActor(c))
	jsonObj(c, rows, err)
}

func (a *AnnouncementController) list(c *gin.Context) {
	rows, err := a.service.List()
	jsonObj(c, rows, err)
}

func (a *AnnouncementController) save(c *gin.Context) {
	input := &service.AnnouncementInput{}
	if err := c.ShouldBindJSON(input); err != nil {
		jsonMsg(c, "保存公告", err)
		return
	}
	row, err := a.service.Save(input)
	jsonMsgObj(c, "保存公告", row, err)
}

func (a *AnnouncementController) delete(c *gin.Context) {
	var input struct {
		Id int `json:"id"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		jsonMsg(c, "删除公告", err)
		return
	}
	jsonMsg(c, "删除公告", a.service.Delete(input.Id))
}
