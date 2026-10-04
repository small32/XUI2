package service

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSystemProxyFilesEnableAndDisableWithoutChangingOtherSettings(t *testing.T) {
	root := t.TempDir()
	other := filepath.Join(root, "etc/apt/apt.conf.d/50existing")
	if err := os.MkdirAll(filepath.Dir(other), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := writeSystemProxyFiles(root, true); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"etc/xui/local-proxy.env", "etc/profile.d/xui-proxy.sh", "etc/apt/apt.conf.d/99xui-proxy", "etc/systemd/system/xui-ssl.service.d/proxy.conf"} {
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil || len(data) == 0 {
			t.Fatalf("missing proxy file %s: %v", name, err)
		}
	}
	data, _ := os.ReadFile(filepath.Join(root, "etc/xui/local-proxy.env"))
	if !strings.Contains(string(data), "https_proxy=http://127.0.0.1:10809") || !strings.Contains(string(data), "socks5h://127.0.0.1:10808") {
		t.Fatal("proxy endpoints incorrect")
	}
	if err := writeSystemProxyFiles(root, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "etc/xui/local-proxy.env")); !os.IsNotExist(err) {
		t.Fatal("proxy environment not removed")
	}
	data, err := os.ReadFile(other)
	if err != nil || string(data) != "keep" {
		t.Fatal("unrelated apt configuration changed")
	}
	if err := writeSystemProxyFiles(root, false); err != nil {
		t.Fatal("repeated disable failed", err)
	}
}
