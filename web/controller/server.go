package controller

import (
	"sync"

	"github.com/gin-gonic/gin"
	"time"
	"xui/config"
	"xui/web/global"
	"xui/web/service"
)

type ServerController struct {
	BaseController

	serverService service.ServerService

	// 以下缓存字段被 cron goroutine（refreshStatus）与 HTTP handler 并发读写，
	// 用 mu 串行化访问，避免数据竞争。
	mu                  sync.RWMutex
	lastStatus          *service.Status
	lastGetStatusTime   time.Time
	lastVersions        []string
	lastGetVersionsTime time.Time
}

func NewServerController(g *gin.RouterGroup) *ServerController {
	a := &ServerController{
		lastGetStatusTime: time.Now(),
	}
	a.initRouter(g)
	a.startTask()
	return a
}

func (a *ServerController) initRouter(g *gin.RouterGroup) {
	g = g.Group("/server")

	// /server 下的接口属管理面，仅允许管理员访问。
	// 受限登录不建立用户会话，会被 checkAdminLogin 拦下，杜绝越权。
	g.Use(a.checkAdminLogin)
	g.POST("/status", a.status)
	if config.Role() == "agent" {
		g.POST("/getXrayVersion", a.getXrayVersion)
		g.POST("/installXray/:version", a.installXray)
	}
}

func (a *ServerController) refreshStatus() {
	a.mu.Lock()
	status := a.serverService.GetStatus(a.lastStatus)
	a.lastStatus = status
	a.mu.Unlock()
}

func (a *ServerController) startTask() {
	webServer := global.GetWebServer()
	c := webServer.GetCron()
	c.AddFunc("@every 2s", func() {
		a.mu.RLock()
		last := a.lastGetStatusTime
		a.mu.RUnlock()
		now := time.Now()
		if now.Sub(last) > time.Minute*3 {
			return
		}
		a.refreshStatus()
	})
}

func (a *ServerController) status(c *gin.Context) {
	a.mu.Lock()
	a.lastGetStatusTime = time.Now()
	status := a.lastStatus
	a.mu.Unlock()

	jsonObj(c, status, nil)
}

func (a *ServerController) getXrayVersion(c *gin.Context) {
	now := time.Now()

	a.mu.RLock()
	lastTime := a.lastGetVersionsTime
	cached := a.lastVersions
	a.mu.RUnlock()
	if now.Sub(lastTime) <= time.Minute {
		jsonObj(c, cached, nil)
		return
	}

	versions, err := a.serverService.GetXrayVersions()
	if err != nil {
		jsonMsg(c, "获取版本", err)
		return
	}

	a.mu.Lock()
	a.lastVersions = versions
	a.lastGetVersionsTime = time.Now()
	a.mu.Unlock()

	jsonObj(c, versions, nil)
}

func (a *ServerController) installXray(c *gin.Context) {
	version := c.Param("version")
	err := a.serverService.UpdateXray(version)
	jsonMsg(c, "安装 xray", err)
}
