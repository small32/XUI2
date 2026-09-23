package controller

import (
	"errors"
	"github.com/gin-gonic/gin"
	"time"
	"xui/config"
	"xui/web/entity"
	"xui/web/service"
	"xui/web/session"
)

type updateUserForm struct {
	OldUsername string `json:"oldUsername" form:"oldUsername"`
	OldPassword string `json:"oldPassword" form:"oldPassword"`
	NewUsername string `json:"newUsername" form:"newUsername"`
	NewPassword string `json:"newPassword" form:"newPassword"`
}

type SettingController struct {
	settingService service.SettingService
	userService    service.UserService
	panelService   service.PanelService
}

func NewSettingController(g *gin.RouterGroup) *SettingController {
	a := &SettingController{}
	a.initRouter(g)
	return a
}

func (a *SettingController) initRouter(g *gin.RouterGroup) {
	g = g.Group("/setting")

	g.POST("/all", a.getAllSetting)
	g.POST("/update", a.updateSetting)
	g.POST("/updateUser", a.updateUser)
	g.POST("/restartPanel", a.restartPanel)
	g.POST("/connectionInfo", a.connectionInfo)
}

func (a *SettingController) getAllSetting(c *gin.Context) {
	allSetting, err := a.settingService.GetAllSetting()
	if err != nil {
		jsonMsg(c, "获取设置", err)
		return
	}
	jsonObj(c, allSetting, nil)
}

// connectionInfo 返回被控端(agent)对外连接信息；仅 agent 角色提供，其余角色返回空，避免泄露令牌与指纹。
func (a *SettingController) connectionInfo(c *gin.Context) {
	if config.Role() != "agent" {
		jsonObj(c, &entity.AgentConnectionInfo{}, nil)
		return
	}
	info, err := a.settingService.AgentConnectionInfo()
	jsonObj(c, info, err)
}

func (a *SettingController) updateSetting(c *gin.Context) {
	allSetting := &entity.AllSetting{}
	err := c.ShouldBind(allSetting)
	if err != nil {
		jsonMsg(c, "修改设置", err)
		return
	}
	err = a.settingService.UpdateAllSetting(allSetting)
	jsonMsg(c, "修改设置", err)
}

func (a *SettingController) updateUser(c *gin.Context) {
	form := &updateUserForm{}
	err := c.ShouldBind(form)
	if err != nil {
		jsonMsg(c, "修改用户", err)
		return
	}
	user := session.GetLoginUser(c)
	// 会话里不保存密码（安全考虑），旧密码校验必须对数据库进行，
	// 不能用会话中的 user.Password（恒为空）。
	if a.userService.CheckUser(form.OldUsername, form.OldPassword) == nil ||
		user.Username != form.OldUsername {
		jsonMsg(c, "修改用户", errors.New("原用户名或原密码错误"))
		return
	}
	if form.NewUsername == "" || form.NewPassword == "" {
		jsonMsg(c, "修改用户", errors.New("新用户名和新密码不能为空"))
		return
	}
	err = a.userService.UpdateUser(user.Id, form.NewUsername, form.NewPassword)
	if err == nil {
		if refreshed := a.userService.CheckUser(form.NewUsername, form.NewPassword); refreshed != nil {
			session.SetLoginUser(c, refreshed)
		}
	}
	jsonMsg(c, "修改用户", err)
}

func (a *SettingController) restartPanel(c *gin.Context) {
	err := a.panelService.RestartPanel(time.Second * 3)
	jsonMsg(c, "重启面板", err)
}
