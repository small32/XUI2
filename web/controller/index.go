package controller

import (
	"net/http"
	"strconv"
	"sync"
	"time"
	"x-ui/config"
	"x-ui/logger"
	"x-ui/web/service"
	"x-ui/web/session"

	"github.com/gin-gonic/gin"
)

// 登录限流：同一 IP 在一段时间内失败多次则临时拒绝，减缓暴力破解。
const (
	loginFailLimit   = 5                // 窗口内允许的最大失败次数
	loginFailWindow  = 10 * time.Minute // 失败窗口
	loginBlockedTime = 10 * time.Minute // 触发后锁定时长
)

type loginFailState struct {
	count int
	first time.Time
}

var (
	loginFailMu   sync.Mutex
	loginFailByIP = map[string]*loginFailState{}
)

// checkLoginRateLimit 返回当前 IP 是否被限流。
func checkLoginRateLimit(ip string) bool {
	loginFailMu.Lock()
	defer loginFailMu.Unlock()
	st, ok := loginFailByIP[ip]
	if !ok {
		return false
	}
	now := time.Now()
	if now.Sub(st.first) > loginBlockedTime && st.count >= loginFailLimit {
		// 锁定期结束，清零释放。
		delete(loginFailByIP, ip)
		return false
	}
	return st.count >= loginFailLimit
}

// recordLoginFail 记录一次登录失败；达到阈值后进入锁定。
func recordLoginFail(ip string) {
	loginFailMu.Lock()
	defer loginFailMu.Unlock()
	now := time.Now()
	st, ok := loginFailByIP[ip]
	if !ok || now.Sub(st.first) > loginFailWindow {
		loginFailByIP[ip] = &loginFailState{count: 1, first: now}
		return
	}
	st.count++
}

// clearLoginFail 登录成功后清零该 IP 的失败记录。
func clearLoginFail(ip string) {
	loginFailMu.Lock()
	defer loginFailMu.Unlock()
	delete(loginFailByIP, ip)
}

type LoginForm struct {
	Username string `json:"username" form:"username"`
	Password string `json:"password" form:"password"`
}

type IndexController struct {
	BaseController

	userService    service.UserService
	inboundService service.InboundService
	settingService service.SettingService
}

func NewIndexController(g *gin.RouterGroup) *IndexController {
	a := &IndexController{}
	a.initRouter(g)
	return a
}

func (a *IndexController) initRouter(g *gin.RouterGroup) {
	g.GET("/", a.index)
	g.POST("/login", a.login)
	g.GET("/logout", a.logout)
}

func (a *IndexController) index(c *gin.Context) {
	if session.IsLogin(c) || session.IsRestricted(c) {
		c.Redirect(http.StatusTemporaryRedirect, "xui/")
		return
	}
	html(c, "login.html", "登录", nil)
}

func (a *IndexController) login(c *gin.Context) {
	ip := getRemoteIp(c)
	if checkLoginRateLimit(ip) {
		logger.Warning("login rate limited, Ip Address:", ip)
		pureJsonMsg(c, false, "尝试过于频繁，请稍后再试")
		return
	}

	var form LoginForm
	err := c.ShouldBind(&form)
	if err != nil {
		pureJsonMsg(c, false, "数据格式错误")
		return
	}
	if form.Username == "" {
		pureJsonMsg(c, false, "请输入用户名")
		return
	}
	if form.Password == "" {
		pureJsonMsg(c, false, "请输入密码")
		return
	}

	// 先按面板管理员账号校验
	user := a.userService.CheckUser(form.Username, form.Password)
	if user != nil {
		clearLoginFail(ip)
		logger.Infof("%s login success,Ip Address:%s\n", form.Username, getRemoteIp(c))
		err = session.SetLoginUser(c, user)
		logger.Info("user", user.Id, "login success")
		jsonMsg(c, "登录", err)
		return
	}

	// 管理员校验失败后，继续尝试受限登录：账号=入站端口号，密码=入站密码。
	// 受限尝试始终执行（不因开关跳过）；开关只决定受限账号命中后是否放行——
	// 命中但开关关闭时明确提示"非管理员登录已禁用"，命中且开启时才建立受限会话。
	if port, convErr := strconv.Atoi(form.Username); config.Role() == "manager" && convErr == nil {
		if inbound := a.inboundService.CheckInboundCredential(port, form.Password); inbound != nil {
			restrictedAllowed, _ := a.settingService.IsRestrictedLoginEnabled()
			if !restrictedAllowed {
				logger.Infof("restricted login disabled, inbound %d tried, Ip Address:%s\n", inbound.Id, getRemoteIp(c))
				pureJsonMsg(c, false, "非管理员登录已禁用")
				return
			}
			clearLoginFail(ip)
			err = session.SetRestrictedLogin(c, inbound.Id, form.Password)
			if err == nil {
				logger.Infof("inbound %d restricted login success, Ip Address:%s\n", inbound.Id, getRemoteIp(c))
			}
			jsonMsg(c, "登录", err)
			return
		}
	}

	recordLoginFail(ip)
	logger.Infof("wrong username or password: username=%q", form.Username)
	pureJsonMsg(c, false, "用户名或密码错误")
}

func (a *IndexController) logout(c *gin.Context) {
	user := session.GetLoginUser(c)
	if user != nil {
		logger.Info("user", user.Id, "logout")
	}
	session.ClearSession(c)
	c.Redirect(http.StatusTemporaryRedirect, c.GetString("base_path"))
}
