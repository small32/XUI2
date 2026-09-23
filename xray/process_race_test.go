package xray

import "testing"

// Start/Stop 会在写锁内改写 cmd / version / apiPort / exitErr，
// 而面板状态查询与流量任务会并发调用 IsRunning / GetVersion / GetErr /
// GetAPIPort / GetResult。这些读写必须都在 p.mu 内完成。
//
// 回归点：一旦读接口去掉锁，go test -race 会立刻报出竞争。
func TestProcessFieldsConcurrentAccess(t *testing.T) {
	p := NewProcess(&Config{})
	const rounds = 5000

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < rounds; i++ {
			_ = p.IsRunning()
			_ = p.GetVersion()
			_ = p.GetErr()
			_ = p.GetAPIPort()
			_ = p.GetResult()
		}
	}()

	for i := 0; i < rounds; i++ {
		p.mu.Lock()
		p.version = "1.0.0"
		p.apiPort = 10085
		p.exitErr = nil
		p.refreshAPIPortLocked()
		p.mu.Unlock()
	}
	<-done
}
