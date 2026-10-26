package controller

import (
	"fmt"
	"github.com/gin-gonic/gin"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"xui/config"
	"xui/database"
	"xui/database/model"
	"xui/web/service"
	"xui/web/session"
)

type ClientController struct {
	BaseController
	service service.ClientService
}

func RegisterClientAPI(g *gin.RouterGroup) {
	if config.Role() != "manager" {
		return
	}
	a := &ClientController{}
	api := g.Group("/api/client/v1")
	api.Use(func(c *gin.Context) { c.Header("Cache-Control", "no-store"); c.Next() })
	api.GET("/info", func(c *gin.Context) {
		v, err := a.service.Settings()
		if err != nil {
			clientError(c, 500, err)
			return
		}
		c.JSON(200, gin.H{"apiVersion": 1, "serverVersion": config.GetVersion(), "enabled": v.Enabled, "serverAddress": v.PublicURL, "defaults": gin.H{"mode": "rule", "systemProxy": true, "tun": false, "tunStack": "mixed", "ipv6": false}})
	})
	api.POST("/auth/login", a.login)
	api.POST("/auth/refresh", a.refresh)
	api.GET("/downloads", a.downloads)
	api.GET("/guide", a.guide)
	api.GET("/release-downloads/:assetID", a.downloadReleaseAsset)
	api.HEAD("/release-downloads/:assetID", a.downloadReleaseAsset)
	api.GET("/release-links/:assetID", a.releaseAssetLink)
	private := api.Group("")
	private.Use(a.authenticate)
	private.POST("/auth/logout", a.logout)
	private.GET("/account", a.account)
	private.GET("/announcements", a.announcements)
	private.POST("/announcements/read", a.announcementsRead)
	private.GET("/subscription/clash", a.subscription)
}
func RegisterClientAdmin(g *gin.RouterGroup) {
	if config.Role() != "manager" {
		return
	}
	a := &ClientController{}
	g = g.Group("/client")
	g.Use(a.checkAdminLogin)
	g.POST("/settings", a.settings)
	g.POST("/settings/update", a.saveSettings)
	g.POST("/guide", a.adminGuide)
	g.POST("/guide/update", a.saveGuide)
	g.POST("/github-release", a.gitHubRelease)
	g.POST("/github/connect", a.connectGitHubApp)
	g.POST("/github/status", a.gitHubAppStatus)
	g.POST("/github/disconnect", a.disconnectGitHubApp)
	g.GET("/github/callback", a.gitHubAppCallback)
	g.GET("/github/installed", a.gitHubAppInstalled)
}
func clientError(c *gin.Context, status int, err error) {
	c.AbortWithStatusJSON(status, gin.H{"error": err.Error()})
}
func (a *ClientController) login(c *gin.Context) {
	ip := getRemoteIp(c)
	if checkLoginRateLimit(ip) {
		clientError(c, 429, fmt.Errorf("登录失败次数过多，请稍后重试"))
		return
	}
	var input struct {
		Port     string `json:"port"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		clientError(c, 400, fmt.Errorf("请输入字符串格式的端口号和密码"))
		return
	}
	tokens, err := a.service.Login(strings.TrimSpace(input.Port), input.Password)
	if err != nil {
		recordLoginFail(ip)
		clientError(c, 401, err)
		return
	}
	clearLoginFail(ip)
	c.JSON(200, tokens)
}
func (a *ClientController) refresh(c *gin.Context) {
	var input struct {
		RefreshToken string `json:"refreshToken"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		clientError(c, 400, err)
		return
	}
	tokens, err := a.service.Refresh(input.RefreshToken)
	if err != nil {
		clientError(c, 401, err)
		return
	}
	c.JSON(200, tokens)
}
func (a *ClientController) authenticate(c *gin.Context) {
	header := c.GetHeader("Authorization")
	if !strings.HasPrefix(header, "Bearer ") {
		clientError(c, 401, fmt.Errorf("请先登录"))
		return
	}
	row, in, err := a.service.Authenticate(strings.TrimPrefix(header, "Bearer "), false)
	if err != nil {
		clientError(c, 401, err)
		return
	}
	c.Set("clientSession", row)
	c.Set("clientInbound", in)
	c.Next()
}
func clientInbound(c *gin.Context) *model.Inbound { return c.MustGet("clientInbound").(*model.Inbound) }
func (a *ClientController) logout(c *gin.Context) {
	row := c.MustGet("clientSession").(*model.ClientSession)
	if err := database.GetDB().Delete(row).Error; err != nil {
		clientError(c, 500, err)
		return
	}
	c.JSON(200, gin.H{"success": true})
}
func (a *ClientController) account(c *gin.Context) {
	v, err := a.service.Account(clientInbound(c), a.baseURL(c))
	if err != nil {
		clientError(c, 500, err)
		return
	}
	c.JSON(200, v)
}
// announcements 返回投放给客户端的通知公告。SClash 在账号轮询时一并拉取，
// 按 id + 内容时间戳去重后以系统通知展示。旧版面板没有该接口时客户端按 404 忽略。
func (a *ClientController) announcements(c *gin.Context) {
	feed, err := new(service.AnnouncementService).ClientFeed()
	if err != nil {
		clientError(c, 500, err)
		return
	}
	c.Header("ETag", fmt.Sprintf("\"%s\"", feed.Version))
	c.JSON(200, feed)
}
// announcementsRead 记录客户端已送达/已读的公告，用于管理端查看触达情况。
// 客户端只需在拿到公告后上报一次；重复上报按首次时间保留，不会产生重复记录。
func (a *ClientController) announcementsRead(c *gin.Context) {
	var input struct {
		Ids []int `json:"ids"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		clientError(c, 400, err)
		return
	}
	in := clientInbound(c)
	if err := new(service.AnnouncementService).MarkRead(service.ClientPortActor(in.Port), service.AnnouncementAudienceClient, input.Ids); err != nil {
		clientError(c, 500, err)
		return
	}
	c.JSON(200, gin.H{"success": true})
}
func (a *ClientController) subscription(c *gin.Context) {
	in := clientInbound(c)
	account, err := a.service.Account(in, a.baseURL(c))
	if err != nil {
		clientError(c, 500, err)
		return
	}
	if !account.CanProxy {
		if account.Status == "invalid_config" {
			clientError(c, 422, fmt.Errorf("%s", account.ConfigError))
			return
		}
		clientError(c, 403, fmt.Errorf("账号当前不可使用代理: %s", account.Status))
		return
	}
	base := a.baseURL(c)
	data, err := a.service.ClashConfig(in, base)
	if err != nil {
		clientError(c, 422, err)
		return
	}
	expire := int64(0)
	if account.ExpiresAt != nil {
		expire = *account.ExpiresAt
	}
	c.Header("Subscription-Userinfo", fmt.Sprintf("upload=0; download=%d; total=%d; expire=%d", account.UsedBytes, account.TotalBytes, expire))
	c.Header("Profile-Update-Interval", "1")
	c.Header("ETag", fmt.Sprintf("\"%s\"", service.ClientConfigHash(data)))
	c.Data(200, "application/yaml; charset=utf-8", data)
}
func (a *ClientController) settings(c *gin.Context) {
	v, err := a.service.Settings()
	jsonObj(c, v, err)
}
func (a *ClientController) saveSettings(c *gin.Context) {
	var input struct {
		service.ClientSettings
		GitHubToken *string `json:"githubToken"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		jsonMsg(c, "保存客户端设置", err)
		return
	}
	jsonMsg(c, "保存客户端设置", a.service.SaveSettings(&input.ClientSettings, input.GitHubToken))
}
func (a *ClientController) guide(c *gin.Context) {
	if a.downloadIdentity(c) == "" {
		return
	}
	value, err := a.service.Guide()
	if err != nil {
		clientError(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, value)
}
func (a *ClientController) adminGuide(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	value, err := a.service.Guide()
	jsonObj(c, value, err)
}
func (a *ClientController) saveGuide(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	// A JSON string can encode each HTML byte as a six-byte Unicode escape.
	const requestLimit = 6*int64(service.ClientGuideMaxSize) + 1024*1024
	if c.Request.ContentLength > requestLimit {
		jsonMsg(c, "保存使用说明", fmt.Errorf("请求过大，使用说明 HTML 最大支持 20MB"))
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, requestLimit)
	var input struct {
		HTML *string `json:"html"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		jsonMsg(c, "保存使用说明", err)
		return
	}
	if input.HTML == nil {
		jsonMsg(c, "保存使用说明", fmt.Errorf("请提供字符串格式的 html"))
		return
	}
	value, err := a.service.SaveGuide(*input.HTML)
	jsonObj(c, value, err)
}
func (a *ClientController) gitHubRelease(c *gin.Context) {
	var input struct {
		Refresh bool `json:"refresh"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		jsonMsg(c, "检查最新版", err)
		return
	}
	value, err := a.service.GitHubRelease(c.Request.Context(), input.Refresh)
	jsonObj(c, value, err)
}
func (a *ClientController) connectGitHubApp(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	var input struct {
		BaseURL string `json:"baseUrl"`
	}
	if err := c.ShouldBindJSON(&input); err != nil && err != io.EOF {
		jsonMsg(c, "连接 GitHub App", fmt.Errorf("连接参数格式无效"))
		return
	}
	baseURL := a.requestBaseURL(c)
	if strings.TrimSpace(input.BaseURL) != "" {
		value, err := service.ValidateClientURL(input.BaseURL)
		if err != nil {
			jsonMsg(c, "连接 GitHub App", err)
			return
		}
		baseURL = value
	}
	value, err := a.service.BeginGitHubApp(c.Request.Context(), session.GetLoginUser(c).Id, baseURL)
	jsonObj(c, value, err)
}
func (a *ClientController) gitHubAppStatus(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	value, err := a.service.GitHubAppStatus()
	jsonObj(c, value, err)
}
func (a *ClientController) disconnectGitHubApp(c *gin.Context) {
	jsonMsg(c, "断开本机 GitHub App 连接", a.service.DisconnectGitHubApp())
}
func (a *ClientController) gitHubAppCallback(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Header("Referrer-Policy", "no-referrer")
	state, code := strings.TrimSpace(c.Query("state")), strings.TrimSpace(c.Query("code"))
	if state == "" || code == "" || c.Query("error") != "" {
		c.String(http.StatusBadRequest, "GitHub 授权未完成或缺少授权参数，请返回面板设置重新连接。")
		return
	}
	installationURL, err := a.service.CompleteGitHubAppRegistration(c.Request.Context(), session.GetLoginUser(c).Id, state, code)
	if err != nil {
		c.String(http.StatusBadRequest, "GitHub 连接失败：%s。请返回面板设置重新连接。", err.Error())
		return
	}
	c.Redirect(http.StatusFound, installationURL)
}
func (a *ClientController) gitHubAppInstalled(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Header("Referrer-Policy", "no-referrer")
	state := strings.TrimSpace(c.Query("state"))
	if state == "" {
		c.String(http.StatusBadRequest, "缺少 GitHub 安装授权状态，请返回面板设置重新连接。")
		return
	}
	if err := a.service.CompleteGitHubAppInstallation(c.Request.Context(), session.GetLoginUser(c).Id, state); err != nil {
		c.String(http.StatusBadRequest, "GitHub 安装连接未完成：%s。请返回面板设置重新连接。", err.Error())
		return
	}
	c.Redirect(http.StatusFound, c.GetString("base_path")+"xui/setting?github=connected")
}
func (a *ClientController) downloadIdentity(c *gin.Context) string {
	if c.GetHeader("Authorization") != "" {
		a.authenticate(c)
		if c.IsAborted() {
			return ""
		}
		return fmt.Sprintf("account:%d", clientInbound(c).Id)
	}
	a.checkLogin(c)
	if c.IsAborted() {
		return ""
	}
	if session.IsRestricted(c) {
		return fmt.Sprintf("account:%d", session.GetLoginInboundId(c))
	}
	return fmt.Sprintf("admin:%d", session.GetLoginUser(c).Id)
}
func (a *ClientController) downloads(c *gin.Context) {
	if a.downloadIdentity(c) == "" {
		return
	}
	out := []gin.H{}
	release, err := a.service.GitHubRelease(c.Request.Context(), false)
	if err != nil {
		c.Header("X-Client-Release-Error", "unavailable")
	} else if release != nil {
		relay, err := a.service.GitHubRelayEnabled()
		if err != nil {
			clientError(c, 500, err)
			return
		}
		cfg, err := a.service.Settings()
		if err != nil {
			clientError(c, 500, err)
			return
		}
		authenticated := false
		if !relay {
			token, err := a.service.GitHubAccessToken(c.Request.Context(), cfg)
			if err != nil {
				c.Header("X-Client-Release-Error", "unavailable")
				c.JSON(200, out)
				return
			}
			authenticated = token != ""
		}
		for _, asset := range release.Assets {
			assetID := strconv.FormatInt(asset.ID, 10)
			relayURL := c.GetString("base_path") + "api/client/v1/release-downloads/" + assetID + "?relay=1"
			link := asset.BrowserDownloadURL
			resolveURL := ""
			if relay {
				link = relayURL
			} else if authenticated {
				resolveURL = c.GetString("base_path") + "api/client/v1/release-links/" + assetID
			}
			out = append(out, gin.H{"id": "github-" + assetID, "platform": asset.Platform, "arch": asset.Arch, "version": release.TagName, "filename": asset.Name, "size": asset.Size, "sha256": asset.SHA256, "url": link, "resolveUrl": resolveURL, "relayUrl": relayURL, "hosted": relay, "source": "github"})
		}
	}
	c.JSON(200, out)
}
func (a *ClientController) baseURL(c *gin.Context) string {
	settings, err := a.service.Settings()
	if err == nil && settings.PublicURL != "" {
		return settings.PublicURL
	}
	return a.requestBaseURL(c)
}
func (a *ClientController) requestBaseURL(c *gin.Context) string {
	scheme := "http"
	if c.Request.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + c.Request.Host + c.GetString("base_path")
}

func (a *ClientController) releaseAssetLink(c *gin.Context) {
	if a.downloadIdentity(c) == "" {
		return
	}
	id, err := strconv.ParseInt(c.Param("assetID"), 10, 64)
	if err != nil || id <= 0 {
		clientError(c, 400, fmt.Errorf("安装包编号无效"))
		return
	}
	link, err := a.service.DirectGitHubAssetURL(c.Request.Context(), id)
	if err != nil {
		clientError(c, 502, err)
		return
	}
	c.JSON(200, gin.H{"url": link})
}
func (a *ClientController) downloadReleaseAsset(c *gin.Context) {
	identity := a.downloadIdentity(c)
	if identity == "" {
		return
	}
	id, err := strconv.ParseInt(c.Param("assetID"), 10, 64)
	if err != nil || id <= 0 {
		clientError(c, 400, fmt.Errorf("安装包编号无效"))
		return
	}
	relay, err := a.service.GitHubRelayEnabled()
	if err != nil {
		clientError(c, 500, err)
		return
	}
	if !relay && c.Query("relay") != "1" {
		link, err := a.service.DirectGitHubAssetURL(c.Request.Context(), id)
		if err != nil {
			clientError(c, 502, err)
			return
		}
		c.Redirect(http.StatusFound, link)
		return
	}
	rangeHeader := c.GetHeader("Range")
	if strings.Contains(rangeHeader, ",") {
		clientError(c, 416, fmt.Errorf("每个下载线程只支持一个字节范围"))
		return
	}
	if c.Request.Method == http.MethodHead {
		release, err := a.service.GitHubRelease(c.Request.Context(), false)
		if err != nil {
			clientError(c, 502, err)
			return
		}
		for _, asset := range release.Assets {
			if asset.ID == id {
				clientAssetHeaders(c, asset.Name, asset.Size)
				c.Header("Accept-Ranges", "bytes")
				c.Status(200)
				return
			}
		}
		clientError(c, 404, fmt.Errorf("安装包已更新或不存在，请刷新下载列表"))
		return
	}
	release, ok := service.AcquireClientRelayDownload(identity)
	if !ok {
		clientError(c, 429, fmt.Errorf("此账号最多支持 5 个并发下载线程"))
		return
	}
	defer release()
	response, asset, err := a.service.OpenGitHubAsset(c.Request.Context(), id, rangeHeader, c.GetHeader("If-Range"))
	if err != nil {
		clientError(c, 502, err)
		return
	}
	defer response.Body.Close()
	clientAssetHeaders(c, asset.Name, asset.Size)
	c.Header("Accept-Ranges", "bytes")
	c.Header("Content-Length", "")
	if response.ContentLength >= 0 {
		c.Header("Content-Length", strconv.FormatInt(response.ContentLength, 10))
	}
	for _, name := range []string{"Content-Range", "ETag", "Last-Modified"} {
		if value := response.Header.Get(name); value != "" {
			c.Header(name, value)
		}
	}
	c.Status(response.StatusCode)
	io.Copy(c.Writer, response.Body)
}
func clientAssetHeaders(c *gin.Context, filename string, size int64) {
	c.Header("Accept-Ranges", "none")
	c.Header("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": filename}))
	c.Header("Content-Length", strconv.FormatInt(size, 10))
	c.Header("Content-Type", "application/octet-stream")
	c.Header("X-Accel-Buffering", "no")
}
