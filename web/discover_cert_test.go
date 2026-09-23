package web

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDiscoverCertIn(t *testing.T) {
	dir := t.TempDir()
	mk := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	// 无证书
	if c, k := discoverCertIn(dir); c != "" || k != "" {
		t.Fatalf("空目录应返回空，got %q %q", c, k)
	}

	// 只有 fullchain.cer，无可配 key：应回落到同名配对（b）
	mk("b.small32.top.cer", "b-cert")
	mk("b.small32.top.key", "b-key")
	c, k := discoverCertIn(dir)
	if c != filepath.Join(dir, "b.small32.top.cer") || k != filepath.Join(dir, "b.small32.top.key") {
		t.Fatalf("同名配对失败 got %q %q", c, k)
	}

	// 添加 fullchain 对后应优先 fullchain
	mk("fullchain.cer", "full-cert")
	mk("fullchain.key", "full-key")
	c, k = discoverCertIn(dir)
	if c != filepath.Join(dir, "fullchain.cer") || k != filepath.Join(dir, "fullchain.key") {
		t.Fatalf("应优先 fullchain，got %q %q", c, k)
	}

	// 孤儿 .cer：无 key，不应选中
	mk("orphan.cer", "x")
	// 多同名配对时取字典序靠前（a.cer < b.small32...）
	mk("a.cer", "a-cert")
	mk("a.key", "a-key")
	// fullchain 仍应优先
	c, k = discoverCertIn(dir)
	if c != filepath.Join(dir, "fullchain.cer") {
		t.Fatalf("仍应优先 fullchain，got %q", c)
	}
}
