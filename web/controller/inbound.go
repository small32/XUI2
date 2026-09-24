package controller

import (
	"fmt"
	"github.com/gin-gonic/gin"
	"strconv"
	"xui/config"
	"xui/database/model"
	"xui/logger"
	"xui/web/entity"
	"xui/web/global"
	"xui/web/service"
	"xui/web/session"
)

type InboundController struct {
	inboundService service.InboundService
	xrayService    service.XrayService
	serverService  service.ServerManagementService
	settingService service.SettingService
	server         *ServerManagementController
}

func NewInboundController(g *gin.RouterGroup, server *ServerManagementController) *InboundController {
	a := &InboundController{server: server}
	a.initRouter(g)
	a.startTask()
	return a
}

func (a *InboundController) initRouter(g *gin.RouterGroup) {
	g = g.Group("/inbound")

	g.POST("/list", a.getInbounds)
	g.POST("/add", a.addInbound)
	g.POST("/del/:id", a.delInbound)
	g.POST("/resetTraffic/:id", a.resetTrafficInbound)
	g.POST("/update/:id", a.updateInbound)
	g.POST("/subscription", a.restrictedSubscription)
}

func (a *InboundController) startTask() {
	if config.Role() == "manager" {
		return
	}
	webServer := global.GetWebServer()
	c := webServer.GetCron()
	c.AddFunc("@every 10s", func() {
		if a.xrayService.IsNeedRestartAndSetFalse() {
			err := a.xrayService.RestartXray(false)
			if err != nil {
				logger.Error("restart xray failed:", err)
			}
		}
	})
}

func (a *InboundController) getInbounds(c *gin.Context) {
	// 受限登录仅返回绑定的那一条入站
	if inboundId := session.GetLoginInboundId(c); inboundId > 0 {
		inbound, err := a.inboundService.GetInbound(inboundId)
		if err != nil {
			jsonMsg(c, "获取", err)
			return
		}
		password, ok := a.inboundService.GetInboundPassword(inboundId)
		if !ok || !session.IsRestrictedCredValid(c, inboundId, password) {
			pureJsonMsg(c, false, "登录信息已过期，请退出后重新登录")
			return
		}
		inbound.Settings, err = service.WithLoginPassword(inbound.Protocol, inbound.Settings, password)
		if err != nil {
			jsonMsg(c, "获取", err)
			return
		}
		if traffic, e := a.serverService.GetTrafficCache(); e == nil {
			for _, row := range traffic {
				if row.Port == inbound.Port {
					inbound.Up, inbound.Down = row.Up, row.Down
					break
				}
			}
		}
		jsonObj(c, []*model.Inbound{inbound}, nil)
		return
	}
	user := session.GetLoginUser(c)
	inbounds, err := a.inboundService.GetInbounds(user.Id)
	if err != nil {
		jsonMsg(c, "获取", err)
		return
	}
	if traffic, e := a.serverService.GetTrafficCache(); e == nil {
		by := map[int]*entity.ServerTraffic{}
		for _, row := range traffic {
			by[row.Port] = row
		}
		for _, in := range inbounds {
			if row := by[in.Port]; row != nil {
				in.Up, in.Down = row.Up, row.Down
			}
		}
	}
	jsonObj(c, inbounds, nil)
}

// restrictedSubscription 受限登录账号获取"生成订阅"所需的只读数据，
// 替代仅管理员可用的 /xui/setting/all 与 /xui/server/inbound/:port。
func (a *InboundController) restrictedSubscription(c *gin.Context) {
	inboundId := session.GetLoginInboundId(c)
	if inboundId <= 0 {
		pureJsonMsg(c, false, "无权访问")
		return
	}
	inbound, err := a.inboundService.GetInbound(inboundId)
	if err != nil {
		jsonMsg(c, "获取", err)
		return
	}
	password, ok := a.inboundService.GetInboundPassword(inboundId)
	if !ok || !session.IsRestrictedCredValid(c, inboundId, password) {
		pureJsonMsg(c, false, "登录信息已过期，请退出后重新登录")
		return
	}
	serverName := ""
	if allSetting, e := a.settingService.GetAllSetting(); e == nil && allSetting != nil {
		serverName = allSetting.ServerName
	}
	var remoteInbounds []map[string]interface{}
	v, e := a.serverService.RemoteInbounds(inbound.Port)
	if e != nil {
		jsonMsg(c, "获取订阅", e)
		return
	}
	for _, node := range v {
		protocol, _ := node["protocol"].(string)
		settings, _ := node["settings"].(string)
		masked, err := service.WithLoginPassword(model.Protocol(protocol), settings, password)
		if err != nil {
			jsonMsg(c, "获取订阅", err)
			return
		}
		node["settings"] = masked
		remoteInbounds = append(remoteInbounds, node)
	}
	jsonObj(c, gin.H{
		"serverName":     serverName,
		"remoteInbounds": remoteInbounds,
	}, nil)
}

func (a *InboundController) addInbound(c *gin.Context) {
	if config.Role() == "agent" {
		pureJsonMsg(c, false, "被控端账号由管理端统一管理")
		return
	}
	inbound := &model.Inbound{}
	err := c.ShouldBind(inbound)
	if err != nil {
		jsonMsg(c, "添加", err)
		return
	}
	if err = a.inboundService.CheckInboundRemark(inbound.Remark); err != nil {
		jsonMsg(c, "添加", err)
		return
	}
	user := session.GetLoginUser(c)
	inbound.UserId = user.Id
	inbound.Enable = true
	inbound.Tag = fmt.Sprintf("inbound-%v", inbound.Port)
	// The agent fills certificate paths from its own panel settings.
	err = a.inboundService.AddInbound(inbound)
	if err == nil {
		a.server.heartbeatSoon()
		if syncErr := a.serverService.SyncInbound(inbound, 0, true); syncErr != nil {
			logger.Warning("被控端账号同步待重试: ", syncErr)
			err = fmt.Errorf("管理端账号已创建，部分被控端同步失败并已进入重试队列: %w", syncErr)
		}
		if config.Role() == "agent" {
			a.xrayService.SetToNeedRestart()
		}
	}
	jsonMsg(c, "添加", err)
}

func (a *InboundController) delInbound(c *gin.Context) {
	if config.Role() == "agent" {
		pureJsonMsg(c, false, "被控端账号由管理端统一管理")
		return
	}
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, "删除", err)
		return
	}
	inbound, err := a.inboundService.GetInbound(id)
	if err != nil {
		jsonMsg(c, "删除", err)
		return
	}
	err = a.inboundService.DelInbound(id)
	if err == nil {
		a.server.heartbeatSoon()
		if config.Role() == "agent" {
			a.xrayService.SetToNeedRestart()
		}
		if syncErr := a.serverService.DeleteSyncedInbound(inbound); syncErr != nil {
			err = fmt.Errorf("管理端账号已删除，部分被控端同步删除失败并已进入重试队列: %w", syncErr)
		}
		// 端口可能被新账号复用：清掉旧端口的流量缓存并强制刷新一次心跳，
		// 避免新账号继承旧账号的缓存用量被自动禁用。
		a.serverService.ResetAccountTrafficCache(inbound.Id)
	}
	jsonMsg(c, "删除", err)
}

func (a *InboundController) resetTrafficInbound(c *gin.Context) {
	if config.Role() == "agent" {
		pureJsonMsg(c, false, "被控端账号由管理端统一管理")
		return
	}
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, "重置流量", err)
		return
	}
	err = a.serverService.ResetAccountTraffic(id)
	jsonMsg(c, "重置流量", err)
}

func (a *InboundController) updateInbound(c *gin.Context) {
	if config.Role() == "agent" {
		pureJsonMsg(c, false, "被控端账号由管理端统一管理")
		return
	}
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		jsonMsg(c, "修改", err)
		return
	}
	inbound := &model.Inbound{
		Id: id,
	}
	err = c.ShouldBind(inbound)
	if err != nil {
		jsonMsg(c, "修改", err)
		return
	}
	// Certificate paths belong to each agent and are applied there.
	// 端口变更需要把远端旧端口账号一并迁移，同步前先取旧端口。
	old, getErr := a.inboundService.GetInbound(id)
	if getErr != nil {
		jsonMsg(c, "修改", getErr)
		return
	}
	if err = a.inboundService.CheckInboundRemark(inbound.Remark); err != nil {
		jsonMsg(c, "修改", err)
		return
	}
	oldPort := old.Port
	err = a.inboundService.UpdateInbound(inbound)
	if err == nil {
		a.server.heartbeatSoon()
		if syncErr := a.serverService.SyncInbound(inbound, oldPort, false); syncErr != nil {
			logger.Warning("被控端账号同步待重试: ", syncErr)
			err = fmt.Errorf("管理端账号已修改，部分被控端同步失败并已进入重试队列: %w", syncErr)
		}
		if config.Role() == "agent" {
			a.xrayService.SetToNeedRestart()
		}
		// Cache is keyed by account ID, so a port change retains its counters.
	}
	jsonMsg(c, "修改", err)
}
