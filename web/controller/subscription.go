package controller

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"xui/web/service"
)

// SubscriptionController 对外提供客户端订阅，由面板自己生成配置，
// 不经过任何外部订阅转换服务。
type SubscriptionController struct {
	BaseController
	inboundService service.InboundService
	subService     service.SubscriptionService
}

// RegisterSubscription 注册公开订阅入口。客户端（Clash）无法携带面板会话，
// 鉴权靠 URL 里的令牌：令牌由入站编号派生，换面板密钥即全部失效。
func RegisterSubscription(g *gin.RouterGroup) {
	a := &SubscriptionController{}
	s := g.Group("/sub")
	s.Use(func(c *gin.Context) { c.Header("Cache-Control", "no-store"); c.Next() })
	s.GET("/:token", a.clash)
}

func (a *SubscriptionController) clash(c *gin.Context) {
	id, err := a.subService.Verify(c.Param("token"))
	if err != nil {
		c.String(http.StatusForbidden, "invalid subscription token")
		return
	}
	inbound, err := a.inboundService.GetInbound(id)
	if err != nil || inbound == nil {
		c.String(http.StatusNotFound, "inbound not found")
		return
	}
	data, err := a.subService.ClashConfig(inbound, c.Request.Host)
	if err != nil {
		c.String(http.StatusInternalServerError, err.Error())
		return
	}
	c.Header("Content-Disposition", `attachment; filename="xui-clash.yaml"`)
	c.Data(http.StatusOK, "text/yaml; charset=utf-8", data)
}
