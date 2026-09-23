package service

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"github.com/shirou/gopsutil/cpu"
	"github.com/shirou/gopsutil/disk"
	"github.com/shirou/gopsutil/host"
	"github.com/shirou/gopsutil/load"
	"github.com/shirou/gopsutil/mem"
	"github.com/shirou/gopsutil/net"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
	"x-ui/logger"
	"x-ui/util/sys"
	"x-ui/xray"
)

type ProcessState string

const (
	Running ProcessState = "running"
	Stop    ProcessState = "stop"
	Error   ProcessState = "error"
)

type Status struct {
	T   time.Time `json:"-"`
	Cpu float64   `json:"cpu"`
	Mem struct {
		Current uint64 `json:"current"`
		Total   uint64 `json:"total"`
	} `json:"mem"`
	Swap struct {
		Current uint64 `json:"current"`
		Total   uint64 `json:"total"`
	} `json:"swap"`
	Disk struct {
		Current uint64 `json:"current"`
		Total   uint64 `json:"total"`
	} `json:"disk"`
	Xray struct {
		State    ProcessState `json:"state"`
		ErrorMsg string       `json:"errorMsg"`
		Version  string       `json:"version"`
	} `json:"xray"`
	Uptime   uint64    `json:"uptime"`
	Loads    []float64 `json:"loads"`
	TcpCount int       `json:"tcpCount"`
	UdpCount int       `json:"udpCount"`
	NetIO    struct {
		Up   uint64 `json:"up"`
		Down uint64 `json:"down"`
	} `json:"netIO"`
	NetTraffic struct {
		Sent uint64 `json:"sent"`
		Recv uint64 `json:"recv"`
	} `json:"netTraffic"`
}

type Release struct {
	TagName string `json:"tag_name"`
}

type ServerService struct {
	xrayService XrayService
}

func (s *ServerService) GetStatus(lastStatus *Status) *Status {
	now := time.Now()
	status := &Status{
		T: now,
	}

	percents, err := cpu.Percent(0, false)
	if err != nil {
		logger.Warning("get cpu percent failed:", err)
	} else {
		status.Cpu = percents[0]
	}

	upTime, err := host.Uptime()
	if err != nil {
		logger.Warning("get uptime failed:", err)
	} else {
		status.Uptime = upTime
	}

	memInfo, err := mem.VirtualMemory()
	if err != nil {
		logger.Warning("get virtual memory failed:", err)
	} else {
		status.Mem.Current = memInfo.Used
		status.Mem.Total = memInfo.Total
	}

	swapInfo, err := mem.SwapMemory()
	if err != nil {
		logger.Warning("get swap memory failed:", err)
	} else {
		status.Swap.Current = swapInfo.Used
		status.Swap.Total = swapInfo.Total
	}

	distInfo, err := disk.Usage("/")
	if err != nil {
		logger.Warning("get dist usage failed:", err)
	} else {
		status.Disk.Current = distInfo.Used
		status.Disk.Total = distInfo.Total
	}

	avgState, err := load.Avg()
	if err != nil {
		logger.Warning("get load avg failed:", err)
	} else {
		status.Loads = []float64{avgState.Load1, avgState.Load5, avgState.Load15}
	}

	ioStats, err := net.IOCounters(false)
	if err != nil {
		logger.Warning("get io counters failed:", err)
	} else if len(ioStats) > 0 {
		ioStat := ioStats[0]
		status.NetTraffic.Sent = ioStat.BytesSent
		status.NetTraffic.Recv = ioStat.BytesRecv

		if lastStatus != nil {
			duration := now.Sub(lastStatus.T)
			seconds := float64(duration) / float64(time.Second)
			up := uint64(float64(status.NetTraffic.Sent-lastStatus.NetTraffic.Sent) / seconds)
			down := uint64(float64(status.NetTraffic.Recv-lastStatus.NetTraffic.Recv) / seconds)
			status.NetIO.Up = up
			status.NetIO.Down = down
		}
	} else {
		logger.Warning("can not find io counters")
	}

	status.TcpCount, err = sys.GetTCPCount()
	if err != nil {
		logger.Warning("get tcp connections failed:", err)
	}

	status.UdpCount, err = sys.GetUDPCount()
	if err != nil {
		logger.Warning("get udp connections failed:", err)
	}

	if s.xrayService.IsXrayRunning() {
		status.Xray.State = Running
		status.Xray.ErrorMsg = ""
	} else {
		err := s.xrayService.GetXrayErr()
		if err != nil {
			status.Xray.State = Error
		} else {
			status.Xray.State = Stop
		}
		status.Xray.ErrorMsg = s.xrayService.GetXrayResult()
	}
	status.Xray.Version = s.xrayService.GetXrayVersion()

	return status
}

func (s *ServerService) GetXrayVersions() ([]string, error) {
	url := "https://api.github.com/repos/XTLS/Xray-core/releases"
	client := &http.Client{Timeout: 20 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return nil, err
	}

	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("获取 Xray 版本失败: HTTP %d", resp.StatusCode)
	}
	buffer := bytes.NewBuffer(make([]byte, 8192))
	buffer.Reset()
	_, err = buffer.ReadFrom(resp.Body)
	if err != nil {
		return nil, err
	}

	releases := make([]Release, 0)
	err = json.Unmarshal(buffer.Bytes(), &releases)
	if err != nil {
		return nil, err
	}
	versions := make([]string, 0, len(releases))
	for _, release := range releases {
		versions = append(versions, release.TagName)
	}
	return versions, nil
}

func (s *ServerService) downloadXRay(version string) (string, error) {
	osName := runtime.GOOS
	arch := runtime.GOARCH

	switch osName {
	case "darwin":
		osName = "macos"
	}

	switch arch {
	case "amd64":
		arch = "64"
	case "arm64":
		arch = "arm64-v8a"
	}

	fileName := fmt.Sprintf("Xray-%s-%s.zip", osName, arch)
	url := fmt.Sprintf("https://github.com/XTLS/Xray-core/releases/download/%s/%s", version, fileName)
	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("下载 Xray %s 失败: HTTP %d", version, resp.StatusCode)
	}

	os.Remove(fileName)
	file, err := os.Create(fileName)
	if err != nil {
		return "", err
	}
	defer file.Close()

	_, err = io.Copy(file, resp.Body)
	if err != nil {
		return "", err
	}

	return fileName, nil
}

func (s *ServerService) UpdateXray(version string) error {
	zipFileName, err := s.downloadXRay(version)
	if err != nil {
		return err
	}

	zipFile, err := os.Open(zipFileName)
	if err != nil {
		return err
	}
	defer func() {
		zipFile.Close()
		os.Remove(zipFileName)
	}()

	stat, err := zipFile.Stat()
	if err != nil {
		return err
	}
	reader, err := zip.NewReader(zipFile, stat.Size())
	if err != nil {
		return err
	}

	stageDir, err := os.MkdirTemp(filepath.Dir(xray.GetBinaryPath()), ".xray-update-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stageDir)
	files := []xrayUpdateFile{
		{zipName: "xray", staged: filepath.Join(stageDir, "xray"), target: xray.GetBinaryPath(), mode: 0755},
		{zipName: "geosite.dat", staged: filepath.Join(stageDir, "geosite.dat"), target: xray.GetGeositePath(), mode: 0644},
		{zipName: "geoip.dat", staged: filepath.Join(stageDir, "geoip.dat"), target: xray.GetGeoipPath(), mode: 0644},
	}
	for _, file := range files {
		if err = extractZipFile(reader, file.zipName, file.staged, file.mode); err != nil {
			return err
		}
	}
	if output, checkErr := exec.Command(files[0].staged, "version").CombinedOutput(); checkErr != nil {
		return fmt.Errorf("新 Xray 文件无法执行: %w: %s", checkErr, strings.TrimSpace(string(output)))
	}

	if s.xrayService.IsXrayRunning() {
		if err = s.xrayService.StopXray(); err != nil {
			return fmt.Errorf("停止 Xray 失败: %w", err)
		}
	}
	backups, err := replaceXrayFiles(files)
	if err != nil {
		if restartErr := s.xrayService.RestartXray(true); restartErr != nil {
			return fmt.Errorf("替换 Xray 文件失败: %v；重新启动原版本失败: %w", err, restartErr)
		}
		return err
	}
	if err = s.xrayService.RestartXray(true); err == nil {
		time.Sleep(500 * time.Millisecond)
		if !s.xrayService.IsXrayRunning() {
			err = fmt.Errorf("新 Xray 启动后立即退出")
		}
	}
	if err != nil {
		rollbackErr := rollbackXrayFiles(backups)
		oldStartErr := s.xrayService.RestartXray(true)
		if rollbackErr != nil || oldStartErr != nil {
			return fmt.Errorf("新 Xray 启动失败: %v；恢复旧版本失败: rollback=%v, restart=%v", err, rollbackErr, oldStartErr)
		}
		return fmt.Errorf("新 Xray 启动失败，已恢复旧版本: %w", err)
	}
	for _, backup := range backups {
		if backup.hadOriginal {
			_ = os.Remove(backup.backup)
		}
	}
	return nil
}

type xrayUpdateFile struct {
	zipName string
	staged  string
	target  string
	mode    fs.FileMode
}

type xrayUpdateBackup struct {
	target      string
	backup      string
	hadOriginal bool
}

func extractZipFile(reader *zip.Reader, name, target string, mode fs.FileMode) error {
	var entry *zip.File
	for _, candidate := range reader.File {
		if candidate.Name == name {
			entry = candidate
			break
		}
	}
	if entry == nil {
		return fmt.Errorf("Xray 更新包缺少 %s", name)
	}
	source, err := entry.Open()
	if err != nil {
		return err
	}
	defer source.Close()
	destination, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(destination, source)
	closeErr := destination.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Chmod(target, mode)
}

func replaceXrayFiles(files []xrayUpdateFile) ([]xrayUpdateBackup, error) {
	backups := make([]xrayUpdateBackup, 0, len(files))
	fail := func(cause error) ([]xrayUpdateBackup, error) {
		if rollbackErr := rollbackXrayFiles(backups); rollbackErr != nil {
			return nil, fmt.Errorf("文件替换失败: %v；恢复旧文件失败: %w", cause, rollbackErr)
		}
		return nil, cause
	}
	for _, file := range files {
		backup := xrayUpdateBackup{target: file.target}
		if _, err := os.Stat(file.target); err == nil {
			placeholder, err := os.CreateTemp(filepath.Dir(file.target), ".xray-backup-")
			if err != nil {
				return fail(err)
			}
			backup.backup = placeholder.Name()
			if err = placeholder.Close(); err != nil {
				_ = os.Remove(backup.backup)
				return fail(err)
			}
			if err = os.Remove(backup.backup); err != nil {
				return fail(err)
			}
			if err = os.Rename(file.target, backup.backup); err != nil {
				return fail(err)
			}
			backup.hadOriginal = true
		} else if !os.IsNotExist(err) {
			return fail(err)
		}
		backups = append(backups, backup)
		if err := os.Rename(file.staged, file.target); err != nil {
			return fail(err)
		}
	}
	return backups, nil
}

func rollbackXrayFiles(backups []xrayUpdateBackup) error {
	var firstErr error
	for i := len(backups) - 1; i >= 0; i-- {
		backup := backups[i]
		if err := os.Remove(backup.target); err != nil && !os.IsNotExist(err) && firstErr == nil {
			firstErr = err
		}
		if backup.hadOriginal {
			if err := os.Rename(backup.backup, backup.target); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}
