package global

import (
	"context"
	"sync"

	"github.com/robfig/cron/v3"
	_ "unsafe"
)

// webServer 在 SIGHUP 重建服务时会被替换，而旧服务的 goroutine（如补发任务）
// 仍可能读取它，必须带锁访问。
var (
	webServerMu sync.RWMutex
	webServer   WebServer
)

type WebServer interface {
	GetCron() *cron.Cron
	GetCtx() context.Context
}

func SetWebServer(s WebServer) {
	webServerMu.Lock()
	defer webServerMu.Unlock()
	webServer = s
}

func GetWebServer() WebServer {
	webServerMu.RLock()
	defer webServerMu.RUnlock()
	return webServer
}
