package controller

import (
	"github.com/gin-gonic/gin"
	"net/http"
	"x-ui/web/session"
)

type BaseController struct {
}

// checkLogin 判定是否存在有效会话：管理员登录或受限登录均可通过。
// 受限登录不建立占位用户（IsRestricted 为真、IsLogin 为假），由后续的
// checkRestricted 白名单进一步限定其可访问范围。
func (a *BaseController) checkLogin(c *gin.Context) {
	if !session.IsLogin(c) && !session.IsRestricted(c) {
		if isAjax(c) {
			pureJsonMsg(c, false, "登录时效已过，请重新登录")
		} else {
			c.Redirect(http.StatusTemporaryRedirect, c.GetString("base_path"))
		}
		c.Abort()
	} else {
		c.Next()
	}
}

// checkAdminLogin 仅允许管理员访问：受限登录（IsLogin 为假）一律拦截，
// 用于 /server 等管理接口，堵住受限账号越权。
func (a *BaseController) checkAdminLogin(c *gin.Context) {
	if !session.IsAdminLogin(c) {
		if isAjax(c) {
			pureJsonMsg(c, false, "无权访问，请使用管理员账号登录")
		} else {
			c.Redirect(http.StatusTemporaryRedirect, c.GetString("base_path"))
		}
		c.Abort()
	} else {
		c.Next()
	}
}
