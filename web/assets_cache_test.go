package web

import (
	"errors"
	"io/fs"
	"strings"
	"testing"
	"testing/fstest"
)

// 生成订阅的规则配置随面板一起发布，必须真的打进二进制：embed 指令写错或文件挪走时，
// 转换后端取到的是 404，订阅会直接失败，而这类问题在页面上看不出来。
func TestSubscriptionConfigAssetIsEmbedded(t *testing.T) {
	const name = "config/ACL4SSR_Mini.ini"
	data, err := fs.ReadFile(&wrapAssetsFS{FS: assetsFS}, name)
	if err != nil {
		t.Fatalf("embedded subscription config: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("embedded subscription config is empty")
	}
	if !strings.Contains(string(data), "[custom]") {
		t.Fatalf("subscription config has no [custom] section: %q", string(data))
	}
}

func TestAssetCacheVersionStableForUnchangedContent(t *testing.T) {
	first := fstest.MapFS{
		"js/client-guide-editor.js": {Data: []byte("window.guide = 'ready';")},
		"css/client-guide.css":      {Data: []byte(".guide { color: blue; }")},
	}
	// Reconstruct the filesystem independently; map iteration must not affect
	// the cache key when paths and bytes are unchanged.
	second := fstest.MapFS{
		"css/client-guide.css":      {Data: []byte(".guide { color: blue; }")},
		"js/client-guide-editor.js": {Data: []byte("window.guide = 'ready';")},
	}
	key, err := assetCacheVersion(first)
	if err != nil || key == "" {
		t.Fatalf("initial asset cache version: key=%q err=%v", key, err)
	}
	for _, assets := range []fs.FS{first, second} {
		got, err := assetCacheVersion(assets)
		if err != nil || got != key {
			t.Fatalf("unchanged assets changed the cache version: got=%q want=%q err=%v", got, key, err)
		}
	}
}

func TestAssetCacheVersionChangesWhenScriptChangesWithoutVersionBump(t *testing.T) {
	assets := fstest.MapFS{
		"js/client-guide-editor.js": {Data: []byte("window.guide = 'old';")},
	}
	before, err := assetCacheVersion(assets)
	if err != nil {
		t.Fatal(err)
	}
	// The application version remains unchanged; only the delivered script
	// bytes change when a release is rebuilt under the same version number.
	assets["js/client-guide-editor.js"].Data = []byte("window.guide = 'new';")
	after, err := assetCacheVersion(assets)
	if err != nil || after == "" || after == before {
		t.Fatalf("changed script retained its old cache version: before=%q after=%q err=%v", before, after, err)
	}
}

func TestAssetCacheVersionIncludesFilePaths(t *testing.T) {
	content := []byte("window.guide = 'ready';")
	before, err := assetCacheVersion(fstest.MapFS{
		"js/client-guide-editor.js": {Data: content},
	})
	if err != nil {
		t.Fatal(err)
	}
	after, err := assetCacheVersion(fstest.MapFS{
		"js/client-guide-html.js": {Data: content},
	})
	if err != nil || after == "" || after == before {
		t.Fatalf("renamed asset retained its old cache version: before=%q after=%q err=%v", before, after, err)
	}
}

type assetCacheReadErrorFS struct {
	fs.FS
}

func (assets assetCacheReadErrorFS) Open(name string) (fs.File, error) {
	if name == "js/client-guide-editor.js" {
		return nil, fs.ErrPermission
	}
	return assets.FS.Open(name)
}

func TestAssetCacheVersionReturnsFileReadErrors(t *testing.T) {
	assets := assetCacheReadErrorFS{FS: fstest.MapFS{
		"js/client-guide-editor.js": {Data: []byte("window.guide = 'ready';")},
	}}
	_, err := assetCacheVersion(assets)
	if err == nil {
		t.Fatal("unreadable asset silently produced a cache version")
	}
	if !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("asset read error lost its cause: %v", err)
	}
}
