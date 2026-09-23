package service

import (
	"testing"

	"x-ui/xray"
)

// 面板状态轮询、10 秒流量任务、xray 存活检测会并发读取 xray 运行状态，
// 与服务重启/停止并发时不得有数据竞争（由 go test -race 判定）。
//
// 回归点：p 与 result 两个包级变量必须始终在 lock 内访问。
func TestXrayServiceStateConcurrentAccess(t *testing.T) {
	s := &XrayService{}
	const rounds = 5000

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < rounds; i++ {
			_ = s.IsXrayRunning()
			_ = s.GetXrayResult()
			_ = s.GetXrayVersion()
			_ = s.GetXrayErr()
		}
	}()

	// 模拟 RestartXray 的写路径：整体在写锁内替换 p 并清空 result 缓存。
	for i := 0; i < rounds; i++ {
		lock.Lock()
		p = xray.NewProcess(&xray.Config{})
		result = ""
		lock.Unlock()
	}
	<-done
}
