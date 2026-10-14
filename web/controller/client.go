package controller

import (
	"fmt"
	"github.com/gin-gonic/gin"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
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
	service    service.ClientService
	packageDir string
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
	api.GET("/downloads/:id", a.download)
	api.HEAD("/downloads/:id", a.download)
	private := api.Group("")
	private.Use(a.authenticate)
	private.POST("/auth/logout", a.logout)
	private.GET("/account", a.account)
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
	g.POST("/packages", a.packages)
	g.POST("/packages/upload", a.upload)
	g.POST("/packages/:id/publish", a.publish)
	g.POST("/packages/:id/delete", a.deletePackage)
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
	v := &service.ClientSettings{}
	if err := c.ShouldBindJSON(v); err != nil {
		jsonMsg(c, "保存客户端设置", err)
		return
	}
	jsonMsg(c, "保存客户端设置", a.service.SaveSettings(v))
}
func (a *ClientController) packages(c *gin.Context) {
	v, err := a.service.Packages(false)
	jsonObj(c, v, err)
}
func (a *ClientController) upload(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, service.ClientPackageMaxSize+1024*1024)
	if err := c.Request.ParseMultipartForm(8 * 1024 * 1024); err != nil {
		jsonMsg(c, "上传安装包", err)
		return
	}
	defer c.Request.MultipartForm.RemoveAll()
	p := model.ClientPackage{Platform: c.PostForm("platform"), Arch: c.PostForm("arch"), Version: c.PostForm("version"), ExternalURL: strings.TrimSpace(c.PostForm("externalUrl")), Published: c.PostForm("published") == "true"}
	var reader io.Reader
	if p.ExternalURL == "" {
		file, header, err := c.Request.FormFile("file")
		if err != nil {
			jsonMsg(c, "上传安装包", err)
			return
		}
		defer file.Close()
		p.Filename = header.Filename
		reader = file
	}
	jsonMsg(c, "上传安装包", a.service.StorePackage(&p, reader, a.packageRoot()))
}
func clientPackageID(c *gin.Context) (int, error) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id < 1 {
		return 0, fmt.Errorf("安装包编号无效")
	}
	return id, nil
}
func (a *ClientController) publish(c *gin.Context) {
	id, err := clientPackageID(c)
	if err != nil {
		jsonMsg(c, "发布安装包", err)
		return
	}
	var input struct {
		Published bool `json:"published"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		jsonMsg(c, "发布安装包", err)
		return
	}
	r := database.GetDB().Model(&model.ClientPackage{}).Where("id = ?", id).Update("published", input.Published)
	if r.Error == nil && r.RowsAffected == 0 {
		r.Error = fmt.Errorf("安装包不存在")
	}
	jsonMsg(c, "发布安装包", r.Error)
}
func (a *ClientController) deletePackage(c *gin.Context) {
	id, err := clientPackageID(c)
	if err == nil {
		err = a.service.DeletePackage(id, a.packageRoot())
	}
	jsonMsg(c, "删除安装包", err)
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
	rows, err := a.service.Packages(true)
	if err != nil {
		clientError(c, 500, err)
		return
	}
	out := []gin.H{}
	for _, p := range rows {
		url := c.GetString("base_path") + "api/client/v1/downloads/" + strconv.Itoa(p.Id)
		if p.ExternalURL != "" {
			url = p.ExternalURL
		}
		out = append(out, gin.H{"id": p.Id, "platform": p.Platform, "arch": p.Arch, "version": p.Version, "filename": p.Filename, "size": p.Size, "sha256": p.SHA256, "url": url, "hosted": p.ExternalURL == ""})
	}
	c.JSON(200, out)
}
func (a *ClientController) download(c *gin.Context) {
	identity := a.downloadIdentity(c)
	if identity == "" {
		return
	}
	id, err := clientPackageID(c)
	if err != nil {
		clientError(c, 400, err)
		return
	}
	var p model.ClientPackage
	if err := database.GetDB().Where("id = ? AND published = ?", id, true).First(&p).Error; err != nil {
		clientError(c, 404, fmt.Errorf("安装包不存在或未发布"))
		return
	}
	if p.ExternalURL != "" {
		c.Redirect(302, p.ExternalURL)
		return
	}
	if strings.Contains(c.GetHeader("Range"), ",") {
		clientError(c, 416, fmt.Errorf("不支持分段下载"))
		return
	}
	file, err := os.Open(filepath.Join(a.packageRoot(), filepath.Base(p.StorageName)))
	if err != nil {
		clientError(c, 404, fmt.Errorf("安装包文件不存在"))
		return
	}
	defer file.Close()
	if c.Request.Method != http.MethodHead {
		release, ok := service.AcquireClientDownload(identity)
		if !ok {
			clientError(c, 429, fmt.Errorf("此账号已有下载进行中，仅允许单线程下载"))
			return
		}
		defer release()
	}
	c.Header("Accept-Ranges", "none")
	c.Header("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": p.Filename}))
	c.Header("Content-Length", strconv.FormatInt(p.Size, 10))
	c.Header("Content-Type", "application/octet-stream")
	c.Header("X-Accel-Buffering", "no")
	if c.Request.Method == http.MethodHead {
		c.Status(200)
		return
	}
	// Ignore a single Range and always send the complete file; never serve 206.
	c.Status(200)
	io.Copy(c.Writer, file)
}

func (a *ClientController) baseURL(c *gin.Context) string {
	settings, err := a.service.Settings()
	if err == nil && settings.PublicURL != "" {
		return settings.PublicURL
	}
	scheme := "http"
	if c.Request.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + c.Request.Host + c.GetString("base_path")
}

func (a *ClientController) packageRoot() string {
	if a.packageDir != "" {
		return a.packageDir
	}
	return service.ClientPackageDir()
}
