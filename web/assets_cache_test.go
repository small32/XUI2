package web

import (
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"

	"github.com/gin-gonic/gin"
)

// 静态资源必须真的能通过 HTTP 取到。http.ServeContent 对未知扩展名会先读一段嗅探
// Content-Type，再 Seek 回起点；只包了 Stat 的 fs.File 在这里会报 "seeker can't seek"，
// 而 .css/.js 有已知 MIME 类型，走不到这段，问题只在非标准扩展名上暴露。
func TestAssetsServedOverHTTP(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.StaticFS("/assets", http.FS(&wrapAssetsFS{FS: assetsFS}))

	for _, name := range []string{"config/ACL4SSR_Mini.ini", "css/client-guide.css", "js/announcement.js"} {
		req := httptest.NewRequest(http.MethodGet, "/assets/"+name, nil)
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: status=%d body=%q", name, w.Code, w.Body.String())
		}
		if w.Body.Len() == 0 {
			t.Fatalf("%s: 返回了空内容", name)
		}
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
