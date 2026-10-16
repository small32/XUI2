package controller

import (
	"html/template"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"xui/config"

	"github.com/gin-contrib/sessions"
	"github.com/gin-contrib/sessions/cookie"
	"github.com/gin-gonic/gin"
)

func TestPanelAssetURLsChangeWithoutVersionBump(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(sessions.Sessions("session", cookie.NewStore([]byte("asset-cache-test-secret"))))
	engine.SetHTMLTemplate(template.Must(template.New("assets").Parse(`<script src="{{.base_path}}assets/js/client-guide-editor.js?{{.cur_ver}}"></script>`)))
	engine.GET("/panel/setting", func(c *gin.Context) {
		c.Set("base_path", "/panel/")
		c.Set("asset_version", c.GetHeader("X-Test-Asset-Version"))
		html(c, "assets", "设置", nil)
	})
	version := config.GetVersion()
	previous := ""
	for _, fingerprint := range []string{"old-assets", "new-assets"} {
		request := httptest.NewRequest(http.MethodGet, "/panel/setting", nil)
		request.Header.Set("X-Test-Asset-Version", fingerprint)
		response := httptest.NewRecorder()
		engine.ServeHTTP(response, request)
		body := response.Body.String()
		want := "/panel/assets/js/client-guide-editor.js?" + version + "-" + fingerprint
		if response.Code != http.StatusOK || !strings.Contains(body, want) {
			t.Fatalf("panel did not version the asset URL: %d %s", response.Code, body)
		}
		if body == previous {
			t.Fatal("changed assets reused the previous browser cache URL")
		}
		previous = body
	}
	if config.GetVersion() != version {
		t.Fatal("asset cache invalidation changed the application version")
	}
}
