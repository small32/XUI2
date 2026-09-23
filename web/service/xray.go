package service

import (
	"encoding/json"
	"errors"
	"sync"
	"x-ui/logger"
	"x-ui/xray"

	"go.uber.org/atomic"
)

// lock 保护下面的包级变量 p 与 result。
// 读路径（面板状态轮询、10 秒流量任务、xray 存活检测等）持读锁，
// 写路径（重启/停止 xray）持写锁。此前读路径完全不加锁，与重启并发时
// 对 p / result 构成真实数据竞争。
//
// 注意：此锁不可重入，已持锁的代码只能调用带 Locked 后缀的内部函数。
var (
	lock             sync.RWMutex
	p                *xray.Process
	result           string
	processGeneration uint64
)

var isNeedXrayRestart atomic.Bool

// isXrayRunningLocked 调用方必须已持有 lock。
func isXrayRunningLocked() bool {
	return p != nil && p.IsRunning()
}

type XrayService struct {
	inboundService InboundService
	settingService SettingService
}

func (s *XrayService) IsXrayRunning() bool {
	lock.RLock()
	defer lock.RUnlock()
	return isXrayRunningLocked()
}

func (s *XrayService) GetXrayErr() error {
	lock.RLock()
	defer lock.RUnlock()
	if p == nil {
		return nil
	}
	return p.GetErr()
}

func (s *XrayService) GetXrayResult() string {
	lock.RLock()
	cached := result
	running := isXrayRunningLocked()
	proc := p
	lock.RUnlock()

	if cached != "" {
		return cached
	}
	if running || proc == nil {
		return ""
	}

	// GetResult 会摘取进程日志，放到锁外执行，避免拖住状态轮询；
	// 取到结果后再加写锁回填缓存。
	res := proc.GetResult()
	lock.Lock()
	result = res
	lock.Unlock()
	return res
}

func (s *XrayService) GetXrayVersion() string {
	lock.RLock()
	defer lock.RUnlock()
	if p == nil {
		return "Unknown"
	}
	return p.GetVersion()
}

func (s *XrayService) GetXrayConfig() (*xray.Config, error) {
	templateConfig, err := s.settingService.GetXrayConfigTemplate()
	if err != nil {
		return nil, err
	}

	xrayConfig := &xray.Config{}
	err = json.Unmarshal([]byte(templateConfig), xrayConfig)
	if err != nil {
		return nil, err
	}

	inbounds, err := s.inboundService.GetAllInbounds()
	if err != nil {
		return nil, err
	}
	for _, inbound := range inbounds {
		if !inbound.Enable {
			continue
		}
		inboundConfig := inbound.GenXrayInboundConfig()
		xrayConfig.InboundConfigs = append(xrayConfig.InboundConfigs, *inboundConfig)
	}
	return xrayConfig, nil
}

func (s *XrayService) GetXrayTraffic() ([]*xray.Traffic, error) {
	traffic, _, err := s.GetXrayTrafficSnapshot()
	return traffic, err
}

// GetXrayTrafficSnapshot returns counters with the identity of the Xray process
// that supplied them. A restarted process starts a fresh counter namespace.
func (s *XrayService) GetXrayTrafficSnapshot() ([]*xray.Traffic, uint64, error) {
	// 一次取到指针与运行状态，避免 IsXrayRunning 与 p 分两次读导致状态错位。
	lock.RLock()
	proc := p
	generation := processGeneration
	running := isXrayRunningLocked()
	lock.RUnlock()

	if !running {
		return nil, generation, errors.New("xray is not running")
	}
	// 读累计值且不重置 xray 计数器。清零由 XrayTrafficJob 在成功落库后推进基线完成，
	// 避免"先清零、写库失败"导致该间隔流量永久丢失。
	traffic, err := proc.GetTraffic(false)
	return traffic, generation, err
}

func (s *XrayService) RestartXray(isForce bool) error {
	lock.Lock()
	defer lock.Unlock()
	logger.Debug("restart xray, force:", isForce)

	xrayConfig, err := s.GetXrayConfig()
	if err != nil {
		return err
	}

	if p != nil && p.IsRunning() {
		if !isForce && p.GetConfig().Equals(xrayConfig) {
			logger.Debug("not need to restart xray")
			return nil
		}
		p.Stop()
	}

	p = xray.NewProcess(xrayConfig)
	processGeneration++
	result = ""
	return p.Start()
}

func (s *XrayService) StopXray() error {
	lock.Lock()
	defer lock.Unlock()
	logger.Debug("stop xray")
	if isXrayRunningLocked() {
		return p.Stop()
	}
	return errors.New("xray is not running")
}

func (s *XrayService) SetToNeedRestart() {
	isNeedXrayRestart.Store(true)
}

func (s *XrayService) IsNeedRestartAndSetFalse() bool {
	return isNeedXrayRestart.CAS(true, false)
}
