package xray

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"
	"xui/util/common"

	"github.com/Workiva/go-datastructures/queue"
	statsservice "github.com/xtls/xray-core/app/stats/command"
	"google.golang.org/grpc"
)

var trafficRegex = regexp.MustCompile("(inbound|outbound)>>>([^>]+)>>>traffic>>>(downlink|uplink)")

func GetBinaryName() string {
	return fmt.Sprintf("xray-%s-%s", runtime.GOOS, runtime.GOARCH)
}

func GetBinaryPath() string {
	return "bin/" + GetBinaryName()
}

func GetConfigPath() string {
	return "bin/config.json"
}

func GetGeositePath() string {
	return "bin/geosite.dat"
}

func GetGeoipPath() string {
	return "bin/geoip.dat"
}

func stopProcess(p *Process) {
	p.Stop()
}

type Process struct {
	*process
}

func NewProcess(xrayConfig *Config) *Process {
	p := &Process{newProcess(xrayConfig)}
	runtime.SetFinalizer(p, stopProcess)
	return p
}

type process struct {
	// mu 保护下面的运行期字段。Start/Stop 走写锁，IsRunning 与各 Get* 走读锁。
	// 此前这些字段完全无保护，而面板状态轮询、10 秒流量任务会在启停过程中
	// 并发读取 cmd / version / apiPort / exitErr，构成真实数据竞争。
	//
	// 注意：此锁不可重入，已持锁的代码只能调用带 Locked 后缀的内部函数。
	mu sync.RWMutex

	cmd     *exec.Cmd
	running bool

	version string
	apiPort int

	config  *Config
	lines   *queue.Queue
	exitErr error
}

func newProcess(config *Config) *process {
	return &process{
		version: "Unknown",
		config:  config,
		lines:   queue.New(100),
	}
}

func (p *process) IsRunning() bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.isRunningLocked()
}

// isRunningLocked 调用方必须已持有 p.mu。
func (p *process) isRunningLocked() bool {
	return p.running
}

func (p *process) GetErr() error {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.exitErr
}

func (p *process) GetResult() string {
	// lines 指向的 queue 自带内部同步，这里只需保护 lines / exitErr 两个字段的读取。
	p.mu.RLock()
	defer p.mu.RUnlock()
	if p.lines.Empty() && p.exitErr != nil {
		return p.exitErr.Error()
	}
	items, _ := p.lines.TakeUntil(func(item interface{}) bool {
		return true
	})
	lines := make([]string, 0, len(items))
	for _, item := range items {
		lines = append(lines, item.(string))
	}
	return strings.Join(lines, "\n")
}

func (p *process) GetVersion() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.version
}

func (p *Process) GetAPIPort() int {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.apiPort
}

func (p *Process) GetConfig() *Config {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.config
}

// refreshAPIPortLocked 调用方必须已持有 p.mu 写锁。
func (p *process) refreshAPIPortLocked() {
	for _, inbound := range p.config.InboundConfigs {
		if inbound.Tag == "api" {
			p.apiPort = inbound.Port
			break
		}
	}
}

// queryVersion 执行 xray -version 取版本号。不触碰进程字段，由调用方在持锁后写入，
// 避免占着写锁做外部命令 IO。
func queryVersion() string {
	cmd := exec.Command(GetBinaryPath(), "-version")
	data, err := cmd.Output()
	if err != nil {
		return "Unknown"
	}
	datas := bytes.Split(data, []byte(" "))
	if len(datas) <= 1 {
		return "Unknown"
	}
	return string(datas[1])
}

func (p *process) Start() (err error) {
	// 版本号查询要执行外部命令，先算好，避免持写锁做 IO。
	version := queryVersion()

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.isRunningLocked() {
		return errors.New("xray is already running")
	}

	defer func() {
		if err != nil {
			p.exitErr = err
		}
	}()

	data, err := json.MarshalIndent(p.config, "", "  ")
	if err != nil {
		return common.NewErrorf("生成 xray 配置文件失败: %v", err)
	}
	configPath := GetConfigPath()
	err = os.WriteFile(configPath, data, 0600)
	if err != nil {
		return common.NewErrorf("写入配置文件失败: %v", err)
	}
	if err = os.Chmod(configPath, 0600); err != nil {
		return common.NewErrorf("限制配置文件权限失败: %v", err)
	}

	cmd := exec.Command(GetBinaryPath(), "-c", configPath)
	p.cmd = cmd

	stdReader, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	errReader, err := cmd.StderrPipe()
	if err != nil {
		return err
	}

	go func() {
		defer func() {
			common.Recover("")
			stdReader.Close()
		}()
		reader := bufio.NewReaderSize(stdReader, 8192)
		for {
			line, _, err := reader.ReadLine()
			if err != nil {
				return
			}
			if p.lines.Len() >= 100 {
				p.lines.Get(1)
			}
			p.lines.Put(string(line))
		}
	}()

	go func() {
		defer func() {
			common.Recover("")
			errReader.Close()
		}()
		reader := bufio.NewReaderSize(errReader, 8192)
		for {
			line, _, err := reader.ReadLine()
			if err != nil {
				return
			}
			if p.lines.Len() >= 100 {
				p.lines.Get(1)
			}
			p.lines.Put(string(line))
		}
	}()

	if err = cmd.Start(); err != nil {
		p.exitErr = err
		return err
	}
	p.running = true
	go func() {
		err := cmd.Wait()
		p.mu.Lock()
		p.running = false
		if err != nil {
			p.exitErr = err
		}
		p.mu.Unlock()
	}()

	p.version = version
	p.refreshAPIPortLocked()

	return nil
}

func (p *process) Stop() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.isRunningLocked() {
		return errors.New("xray is not running")
	}
	return p.cmd.Process.Kill()
}

func (p *process) GetTraffic(reset bool) ([]*Traffic, error) {
	// 取一次端口快照，后续网络请求在锁外执行，避免长时间占锁。
	p.mu.RLock()
	apiPort := p.apiPort
	p.mu.RUnlock()

	if apiPort == 0 {
		return nil, common.NewError("xray api port wrong:", apiPort)
	}
	conn, err := grpc.Dial(fmt.Sprintf("127.0.0.1:%v", apiPort), grpc.WithInsecure())
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	client := statsservice.NewStatsServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
	defer cancel()
	request := &statsservice.QueryStatsRequest{
		Reset_: reset,
	}
	resp, err := client.QueryStats(ctx, request)
	if err != nil {
		return nil, err
	}
	tagTrafficMap := map[string]*Traffic{}
	traffics := make([]*Traffic, 0)
	for _, stat := range resp.GetStat() {
		matchs := trafficRegex.FindStringSubmatch(stat.Name)
		// xray 会返回 user 级统计等不符合本正则的项，matchs 为 nil，
		// 直接取下标会触发 index out of range panic，导致整个面板崩溃，故判空跳过。
		if len(matchs) < 4 {
			continue
		}
		isInbound := matchs[1] == "inbound"
		tag := matchs[2]
		isDown := matchs[3] == "downlink"
		if tag == "api" {
			continue
		}
		traffic, ok := tagTrafficMap[tag]
		if !ok {
			traffic = &Traffic{
				IsInbound: isInbound,
				Tag:       tag,
			}
			tagTrafficMap[tag] = traffic
			traffics = append(traffics, traffic)
		}
		if isDown {
			traffic.Down = stat.Value
		} else {
			traffic.Up = stat.Value
		}
	}

	return traffics, nil
}
