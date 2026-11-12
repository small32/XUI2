package controller

import (
	"net/http"
	"net/url"
	"strings"

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
	// 订阅显示名用入站列表里的用户名（remark）。客户端优先读 filename*，
	// 按 RFC 5987 做百分号编码，中文用户名也能正常显示。
	c.Header("Content-Disposition", subscriptionDisposition(inbound.Remark))
	c.Data(http.StatusOK, "text/yaml; charset=utf-8", data)
}

// subscriptionDisposition 生成订阅的文件名头：filename* 带真实用户名，
// filename 留一个 ASCII 兜底，避免老客户端把整串百分号编码当成名字。
func subscriptionDisposition(remark string) string {
	name := strings.TrimSpace(remark)
	// 去掉控制字符和会破坏头部的符号
	name = strings.Map(func(r rune) rune {
		if r < 0x20 || r == '"' || r == '\\' || r == '/' || r == ';' {
			return -1
		}
		return r
	}, name)
	if runes := []rune(name); len(runes) > 64 {
		name = string(runes[:64])
	}
	if name == "" {
		name = "XUI"
	}
	// HTTP 头只能是 ASCII，非 ASCII 用户名只放在 filename* 里，filename 退回 XUI 兜底
	ascii := name
	for _, r := range name {
		if r > 0x7F {
			ascii = "XUI"
			break
		}
	}
	return `attachment; filename="` + ascii + `"; filename*=UTF-8''` + url.PathEscape(name)
}
