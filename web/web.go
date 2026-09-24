package web

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"embed"
	"html/template"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"xui/config"
	"xui/logger"
	"xui/util/common"
	"xui/web/controller"
	"xui/web/job"
	"xui/web/network"
	"xui/web/service"

	"github.com/BurntSushi/toml"
	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
	"github.com/nicksnyder/go-i18n/v2/i18n"
	"github.com/robfig/cron/v3"
	"golang.org/x/text/language"
)

//go:embed assets/*
var assetsFS embed.FS

//go:embed html/*
var htmlFS embed.FS

//go:embed translation/*
var i18nFS embed.FS

var startTime = time.Now()

type wrapAssetsFS struct {
	embed.FS
}

func (f *wrapAssetsFS) Open(name string) (fs.File, error) {
	file, err := f.FS.Open("assets/" + name)
	if err != nil {
		return nil, err
	}
	return &wrapAssetsFile{
		File: file,
	}, nil
}

type wrapAssetsFile struct {
	fs.File
}

func (f *wrapAssetsFile) Stat() (fs.FileInfo, error) {
	info, err := f.File.Stat()
	if err != nil {
		return nil, err
	}
	return &wrapAssetsFileInfo{
		FileInfo: info,
	}, nil
}

type wrapAssetsFileInfo struct {
	fs.FileInfo
}

func (f *wrapAssetsFileInfo) ModTime() time.Time {
	return startTime
}

type Server struct {
	httpServer *http.Server
	listener   net.Listener

	index  *controller.IndexController
	server *controller.ServerController
	xui    *controller.XUIController

	xrayService    service.XrayService
	settingService service.SettingService
	inboundService service.InboundService

	cron *cron.Cron

	ctx    context.Context
	cancel context.CancelFunc
}

func NewServer() *Server {
	ctx, cancel := context.WithCancel(context.Background())
	return &Server{
		ctx:    ctx,
		cancel: cancel,
	}
}

func (s *Server) getHtmlFiles() ([]string, error) {
	files := make([]string, 0)
	dir, _ := os.Getwd()
	err := fs.WalkDir(os.DirFS(dir), "web/html", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return files, nil
}

func (s *Server) getHtmlTemplate(funcMap template.FuncMap) (*template.Template, error) {
	t := template.New("").Funcs(funcMap)
	err := fs.WalkDir(htmlFS, "html", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}

		if d.IsDir() {
			newT, err := t.ParseFS(htmlFS, path+"/*.html")
			if err != nil {
				// ignore
				return nil
			}
			t = newT
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return t, nil
}

func (s *Server) initRouter() (*gin.Engine, error) {
	if config.IsDebug() {
		gin.SetMode(gin.DebugMode)
	} else {
		gin.DefaultWriter = io.Discard
		gin.DefaultErrorWriter = io.Discard
		gin.SetMode(gin.ReleaseMode)
	}

	engine := gin.Default()

	secret, err := s.settingService.GetSecret()
	if err != nil {
		return nil, err
	}

	basePath, err := s.settingService.GetBasePath()
	if err != nil {
		return nil, err
	}
	assetsBasePath := basePath + "assets/"

	store := newSessionStore(secret)
	engine.Use(sessions.Sessions("session", store))
	engine.Use(func(c *gin.Context) {
		c.Set("base_path", basePath)
	})
	engine.Use(func(c *gin.Context) {
		uri := c.Request.RequestURI
		if strings.HasPrefix(uri, assetsBasePath) {
			c.Header("Cache-Control", "max-age=31536000")
		}
	})
	err = s.initI18n(engine)
	if err != nil {
		return nil, err
	}

	if config.IsDebug() {
		// for develop
		files, err := s.getHtmlFiles()
		if err != nil {
			return nil, err
		}
		engine.LoadHTMLFiles(files...)
		engine.StaticFS(basePath+"assets", http.FS(os.DirFS("web/assets")))
	} else {
		// for prod
		t, err := s.getHtmlTemplate(engine.FuncMap)
		if err != nil {
			return nil, err
		}
		engine.SetHTMLTemplate(t)
		engine.StaticFS(basePath+"assets", http.FS(&wrapAssetsFS{FS: assetsFS}))
	}

	g := engine.Group(basePath)

	s.index = controller.NewIndexController(g)
	s.server = controller.NewServerController(g)
	s.xui = controller.NewXUIController(g)
	if config.Role() == "agent" {
		controller.RegisterAgentAPI(engine)
	}

	return engine, nil
}

func newSessionStore(secret []byte) sessions.Store {
	// Cookie store accepts an authentication key and an optional encryption key.
	// Derive a distinct 256-bit encryption key so session values (including the
	// password-change fingerprint) are not readable from a signed cookie.
	encryptionKey := sha256.Sum256(append([]byte("xui/session-encryption/"), secret...))
	store := cookie.NewStore(secret, encryptionKey[:])
	store.Options(sessions.Options{
		Path:     "/",
		MaxAge:   60 * 60 * 24 * 30,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	return store
}

func (s *Server) initI18n(engine *gin.Engine) error {
	bundle := i18n.NewBundle(language.SimplifiedChinese)
	bundle.RegisterUnmarshalFunc("toml", toml.Unmarshal)
	err := fs.WalkDir(i18nFS, "translation", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := i18nFS.ReadFile(path)
		if err != nil {
			return err
		}
		_, err = bundle.ParseMessageFileBytes(data, path)
		return err
	})
	if err != nil {
		return err
	}

	engine.Use(func(c *gin.Context) {
		accept := c.GetHeader("Accept-Language")
		c.Set("localizer", i18n.NewLocalizer(bundle, accept))
		c.Next()
	})

	return nil
}

func (s *Server) startTask() {
	if config.Role() == "manager" {
		return
	}
	if err := service.MaybeAgentMonthlyReset(); err != nil {
		logger.Warning("被控端月度流量留档失败: ", err)
	}
	err := s.xrayService.RestartXray(true)
	if err != nil {
		logger.Warning("start xray failed:", err)
	}
	// 每 30 秒检查一次 xray 是否在运行
	s.cron.AddJob("@every 30s", job.NewCheckXrayRunningJob())

	// 每 10 秒统计一次流量。同步注册，避免此前延迟 goroutine 在
	// SIGHUP 重启后向已停止/新实例的 cron 迟到注册造成竞态。
	// xray 尚未就绪时 GetTraffic 内部会报错并记 Warning，下一轮自动重试。
	s.cron.AddJob("@every 10s", job.NewXrayTrafficJob())

	// 每 30 秒检查一次 inbound 流量超出和到期的情况
	s.cron.AddJob("@every 30s", job.NewCheckInboundJob())
	s.cron.AddFunc("@every 5m", func() {
		if err := service.MaybeAgentMonthlyReset(); err != nil {
			logger.Warning("被控端月度流量留档失败: ", err)
		}
	})
}

func (s *Server) Start() (err error) {
	//这是一个匿名函数，没没有函数名
	defer func() {
		if err != nil {
			s.Stop()
		}
	}()

	// 定时任务（含月度清零）统一按上海时区触发，与业务内的月份推算保持一致，
	// 不依赖服务器系统时区。
	s.cron = cron.New(cron.WithLocation(common.ShanghaiLocation), cron.WithSeconds())
	s.cron.Start()

	engine, err := s.initRouter()
	if err != nil {
		return err
	}

	certFile, keyFile, err := s.resolvePanelCert()
	if err != nil {
		return err
	}
	listen, err := s.settingService.GetListen()
	if err != nil {
		return err
	}
	port, err := s.settingService.GetPort()
	if err != nil {
		return err
	}
	listenAddr := net.JoinHostPort(listen, strconv.Itoa(port))
	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return err
	}
	if certFile != "" || keyFile != "" {
		cert, err := tls.LoadX509KeyPair(certFile, keyFile)
		if err != nil {
			listener.Close()
			return err
		}
		service.SetActivePanelCertificate(cert.Certificate[0])
		c := &tls.Config{
			Certificates: []tls.Certificate{cert},
		}
		listener = network.NewAutoHttpsListener(listener)
		listener = tls.NewListener(listener, c)
	} else {
		service.SetActivePanelCertificate(nil)
	}

	if certFile != "" || keyFile != "" {
		logger.Info("web server run https on", listener.Addr())
	} else {
		logger.Info("web server run http on", listener.Addr())
	}
	s.listener = listener

	s.startTask()

	s.httpServer = &http.Server{
		Handler: engine,
	}

	go func() {
		s.httpServer.Serve(listener)
	}()

	return nil
}

// 安装脚本生成的兜底自签证书位置：/root/cert 下没有可用证书时用它。
const (
	selfSignedCert = "/etc/xui/panel.crt"
	selfSignedKey  = "/etc/xui/panel.key"
)

// resolvePanelCert 每次启动重新识别一次面板证书：
//  1. 优先 /root/cert 下由一键申请生成的可信证书，识别到就回填设置，
//     设置页据此显示实际生效的路径；
//  2. 识别不到时沿用设置中的证书，通常是安装脚本生成的自签证书；
//  3. 设置中的路径已不可用（例如证书被删）时退回自签证书，避免面板起不来。
func (s *Server) resolvePanelCert() (string, string, error) {
	if certFile, keyFile := discoverRootCert(); canUseCertPair(certFile, keyFile) {
		logger.Info("auto-using cert from /root/cert: ", certFile, " / ", keyFile)
		if err := s.settingService.SetCertFiles(certFile, keyFile); err != nil {
			logger.Warning("回填面板证书设置失败: ", err)
		}
		return certFile, keyFile, nil
	}
	certFile, err := s.settingService.GetCertFile()
	if err != nil {
		return "", "", err
	}
	keyFile, err := s.settingService.GetKeyFile()
	if err != nil {
		return "", "", err
	}
	if certFile == "" || keyFile == "" || canUseCertPair(certFile, keyFile) {
		return certFile, keyFile, nil
	}
	if canUseCertPair(selfSignedCert, selfSignedKey) {
		logger.Warning("面板证书不可用，改用自签证书: ", selfSignedCert, " / ", selfSignedKey)
		return selfSignedCert, selfSignedKey, nil
	}
	// 两处都不可用：返回原路径，由调用方报出确切错误，不静默退化成 HTTP。
	return certFile, keyFile, nil
}

// canUseCertPair 判断证书与私钥是否齐备且能被加载。
func canUseCertPair(certFile, keyFile string) bool {
	if certFile == "" || keyFile == "" {
		return false
	}
	_, err := tls.LoadX509KeyPair(certFile, keyFile)
	return err == nil
}

// discoverRootCert 扫描 /root/cert 目录，尝试为面板找到一对证书与私钥。
func discoverRootCert() (certFile, keyFile string) {
	return discoverCertIn("/root/cert")
}

// discoverCertIn 在给定目录中查找一对面板证书与私钥。
// 优先返回 fullchain.cer + fullchain.key；否则按同名 .cer/.key 配对。
// 找不到可用的证书对时返回两个空串。
func discoverCertIn(dir string) (certFile, keyFile string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", ""
	}
	// 1) 优先：fullchain.cer 配 fullchain.key；key 缺省时取目录中第一个 *.key
	//    （菜单 16 安装的私钥名为 <域名>.key），保证 TLS 与指纹展示同用这张完整链证书。
	if c := filepath.Join(dir, "fullchain.cer"); fileExists(c) {
		if k := filepath.Join(dir, "fullchain.key"); fileExists(k) {
			return c, k
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".key") {
				continue
			}
			return c, filepath.Join(dir, e.Name())
		}
	}
	// 2) 同名配对的 .cer 与 .key：遍历目录条目
	type pair struct{ cert, key string }
	pairs := make([]pair, 0)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if filepath.Ext(name) == ".cer" {
			base := strings.TrimSuffix(name, ".cer")
			k := filepath.Join(dir, base+".key")
			if fileExists(k) {
				pairs = append(pairs, pair{cert: filepath.Join(dir, name), key: k})
			}
		}
	}
	if len(pairs) > 0 {
		// 若有 fullchain.cer/fullchain.key 之外的匹配，取字典序靠前的一个，保持确定性
		sort.Slice(pairs, func(i, j int) bool { return pairs[i].cert < pairs[j].cert })
		return pairs[0].cert, pairs[0].key
	}
	return "", ""
}

// fileExists 报告路径是否为存在的常规文件。
func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func (s *Server) Stop() error {
	s.cancel()
	if config.Role() == "agent" {
		s.xrayService.StopXray()
	}
	if s.cron != nil {
		s.cron.Stop()
	}
	var err1 error
	var err2 error
	if s.httpServer != nil {
		// s.ctx 已被 s.cancel() 取消，不能用作 Shutdown 的宽限上下文，
		// 否则会立即中断在途请求。这里用一个独立的超时上下文，给
		// 正在处理的请求留出有限宽限时间完成（优雅关闭）。
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err1 = s.httpServer.Shutdown(ctx)
	}
	if s.listener != nil {
		err2 = s.listener.Close()
	}
	return common.Combine(err1, err2)
}

func (s *Server) GetCtx() context.Context {
	return s.ctx
}

func (s *Server) GetCron() *cron.Cron {
	return s.cron
}
