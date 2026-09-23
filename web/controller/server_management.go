package controller

import (
	"strconv"
	"sync"
	"time"
	"xui/config"
	"xui/logger"
	"xui/web/entity"
	"xui/web/global"
	"xui/web/service"
	"xui/web/session"

	"github.com/gin-gonic/gin"
)

const monthlyResetCronSpec = "1 0 0 1 * *"
const monthlyResetFallbackSpec = "@every 5m"

type ServerManagementController struct {
	service       service.ServerManagementService
	lastHeartbeat time.Time
	heartbeatMu   sync.Mutex
}

func NewServerManagementController(g *gin.RouterGroup) *ServerManagementController {
	a := &ServerManagementController{}
	if config.Role() != "manager" {
		return a
	}
	g.GET("/server", a.page)
	g.POST("/server/setting", a.setting)
	g.POST("/server/setting/all", a.getSetting)
	g.POST("/server/nodes", a.nodes)
	g.POST("/server/nodes/save", a.saveNode)
	g.POST("/server/nodes/delete/:id", a.deleteNode)
	g.POST("/server/tasks", a.tasks)
	g.POST("/server/traffic", a.traffic)
	g.GET("/traffic-summary", a.summaryPage)
	g.POST("/traffic-summary/list", a.summary)
	g.POST("/traffic-summary/snapshots", a.resetSnapshots)
	g.POST("/traffic-summary/nodes/:port", a.nodeUsage)
	g.POST("/server/inbound/:port", a.remoteInbounds)
	cron := global.GetWebServer().GetCron()
	cron.AddFunc(monthlyResetCronSpec, a.monthlyReset)
	cron.AddFunc(monthlyResetFallbackSpec, a.monthlyReset)
	cron.AddFunc("@every 1m", func() {
		setting, err := a.service.GetSetting()
		if err != nil {
			logger.Warning("管理端设置读取失败: ", err)
			return
		}
		a.heartbeatMu.Lock()
		if !a.lastHeartbeat.IsZero() && time.Since(a.lastHeartbeat) < time.Duration(setting.HeartbeatMinutes)*time.Minute {
			a.heartbeatMu.Unlock()
			return
		}
		a.lastHeartbeat = time.Now()
		a.heartbeatMu.Unlock()
		if err := a.service.MaybeMonthlyReset(); err != nil {
			logger.Warning("月度重置失败: ", err)
		}
		if err := a.service.DispatchPending(); err != nil {
			logger.Warning("账号同步重试失败: ", err)
		}
		if _, err := a.service.Traffic(); err != nil {
			logger.Warning("被控端流量读取失败: ", err)
		}
	})
	return a
}
func (a *ServerManagementController) page(c *gin.Context) {
	html(c, "server.html", "被控端管理", nil)
}
func (a *ServerManagementController) setting(c *gin.Context) {
	var v entity.ServerSetting
	if err := c.ShouldBindJSON(&v); err != nil {
		jsonMsg(c, "保存设置", err)
		return
	}
	jsonMsg(c, "保存设置", a.service.SaveSetting(&v))
}
func (a *ServerManagementController) getSetting(c *gin.Context) {
	v, err := a.service.GetSetting()
	jsonObj(c, v, err)
}
func (a *ServerManagementController) nodes(c *gin.Context) {
	v, err := a.service.Nodes()
	jsonObj(c, v, err)
}
func (a *ServerManagementController) saveNode(c *gin.Context) {
	var input service.NodeInput
	if err := c.ShouldBindJSON(&input); err != nil {
		jsonMsg(c, "保存被控端", err)
		return
	}
	if err := a.service.SaveNode(input); err != nil {
		jsonMsg(c, "保存被控端", err)
		return
	}
	if err := a.service.DispatchPending(); err != nil {
		logger.Warning("被控端初始同步待重试: ", err)
	}
	jsonMsg(c, "保存被控端", nil)
}
func (a *ServerManagementController) deleteNode(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, "删除被控端", err)
		return
	}
	jsonMsg(c, "删除被控端", a.service.DeleteNode(id))
}
func (a *ServerManagementController) tasks(c *gin.Context) {
	v, err := a.service.PendingTasks()
	jsonObj(c, v, err)
}
func (a *ServerManagementController) traffic(c *gin.Context) {
	v, err := a.service.Traffic()
	jsonObj(c, v, err)
}
func (a *ServerManagementController) summaryPage(c *gin.Context) {
	html(c, "traffic_summary.html", "流量汇总", nil)
}
func (a *ServerManagementController) summary(c *gin.Context) {
	v, err := a.service.Summary(session.GetLoginInboundId(c))
	jsonObj(c, v, err)
}
func (a *ServerManagementController) monthlyReset() {
	if err := a.service.MaybeMonthlyReset(); err != nil {
		logger.Warning("月度重置失败: ", err)
	}
}
func (a *ServerManagementController) resetSnapshots(c *gin.Context) {
	var form struct {
		InboundId int `json:"inboundId"`
	}
	_ = c.ShouldBindJSON(&form)
	if bound := session.GetLoginInboundId(c); bound > 0 {
		form.InboundId = bound
	}
	v, err := a.service.TrafficResetSnapshots(form.InboundId)
	jsonObj(c, v, err)
}
func (a *ServerManagementController) nodeUsage(c *gin.Context) {
	port, err := strconv.Atoi(c.Param("port"))
	if err != nil {
		jsonMsg(c, "读取节点流量", err)
		return
	}
	if bound := session.GetLoginInboundId(c); bound > 0 {
		rows, e := a.service.Summary(bound)
		if e != nil {
			jsonMsg(c, "读取节点流量", e)
			return
		}
		if len(rows) != 1 || rows[0].Port != port {
			pureJsonMsg(c, false, "无权查看其他账号")
			return
		}
	}
	v, err := a.service.NodeUsage(port)
	jsonObj(c, v, err)
}
func (a *ServerManagementController) remoteInbounds(c *gin.Context) {
	port, err := strconv.Atoi(c.Param("port"))
	if err != nil {
		jsonMsg(c, "读取节点", err)
		return
	}
	v, err := a.service.RemoteInbounds(port)
	jsonObj(c, v, err)
}
