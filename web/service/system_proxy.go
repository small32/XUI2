package service

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
	"xui/xray"
)

// System proxy files are owned by XUI; disabling leaves unrelated settings intact.
func writeSystemProxyFiles(root string, enabled bool) error {
	proxy := fmt.Sprintf("http://127.0.0.1:%d", xray.LocalHTTPPort)
	env := fmt.Sprintf("http_proxy=%s\nhttps_proxy=%s\nHTTP_PROXY=%s\nHTTPS_PROXY=%s\nall_proxy=socks5h://127.0.0.1:%d\nALL_PROXY=socks5h://127.0.0.1:%d\nno_proxy=localhost,127.0.0.1,::1\nNO_PROXY=localhost,127.0.0.1,::1\n", proxy, proxy, proxy, proxy, xray.LocalSOCKSPort, xray.LocalSOCKSPort)
	files := map[string]string{
		"etc/xui/local-proxy.env":                         env,
		"etc/profile.d/xui-proxy.sh":                      "# Managed by XUI; loaded by new login shells.\nif [ -r /etc/xui/local-proxy.env ]; then\n  set -a\n  . /etc/xui/local-proxy.env\n  set +a\nfi\n",
		"etc/apt/apt.conf.d/99xui-proxy":                  fmt.Sprintf("Acquire::http::Proxy \"%s\";\nAcquire::https::Proxy \"%s\";\n", proxy, proxy),
		"etc/systemd/system/xui-ssl.service.d/proxy.conf": "[Service]\nEnvironmentFile=-/etc/xui/local-proxy.env\n",
	}
	for name, content := range files {
		path := filepath.Join(root, name)
		if !enabled {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			return err
		}
		tmp, err := os.CreateTemp(filepath.Dir(path), ".xui-proxy-*")
		if err != nil {
			return err
		}
		_, err = tmp.WriteString(content)
		if err == nil {
			err = tmp.Chmod(0644)
		}
		closeErr := tmp.Close()
		if err == nil {
			err = closeErr
		}
		if err == nil {
			err = os.Rename(tmp.Name(), path)
		}
		if err != nil {
			os.Remove(tmp.Name())
			return err
		}
	}
	return nil
}

func ApplySystemProxy(enabled bool) error {
	if runtime.GOOS != "linux" {
		if enabled {
			return fmt.Errorf("本机系统代理仅支持 Linux")
		}
		return nil
	}
	if !enabled {
		found := false
		for _, name := range []string{"/etc/xui/local-proxy.env", "/etc/profile.d/xui-proxy.sh", "/etc/apt/apt.conf.d/99xui-proxy", "/etc/systemd/system/xui-ssl.service.d/proxy.conf"} {
			if _, err := os.Stat(name); err == nil {
				found = true
				break
			}
		}
		if !found {
			return nil
		}
	}
	if enabled {
		for _, port := range []int{xray.LocalHTTPPort, xray.LocalSOCKSPort} {
			var err error
			for attempt := 0; attempt < 20; attempt++ {
				var c net.Conn
				c, err = net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
				if err == nil {
					c.Close()
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
			if err != nil {
				return fmt.Errorf("本机代理端口 %d 未启动: %w", port, err)
			}
		}
	}
	if err := writeSystemProxyFiles("/", enabled); err != nil {
		_ = writeSystemProxyFiles("/", false)
		return err
	}
	if _, err := exec.LookPath("systemctl"); err == nil {
		cmd := exec.Command("systemctl", "daemon-reload")
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("更新证书续期代理环境失败: %s: %w", out, err)
		}
	}
	return nil
}
