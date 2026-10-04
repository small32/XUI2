package controller

import (
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"xui/database"
	"xui/database/model"
	"xui/web/service"

	"github.com/gin-gonic/gin"
)

func TestAgentAllInboundsIncludesLocalAndManaged(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "read.db")); err != nil {
		t.Fatal(err)
	}
	sqlDB, err := database.GetDB().DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	for _, in := range []model.Inbound{
		{Port: 32101, Tag: "local", Protocol: model.VLESS},
		{Port: 32102, Tag: "managed", Protocol: model.Trojan, ManagerAccountID: 42},
	} {
		if err := database.GetDB().Create(&in).Error; err != nil {
			t.Fatal(err)
		}
	}
	token := strings.Repeat("x", 64)
	t.Setenv("XUI_AGENT_TOKEN", token)
	gin.SetMode(gin.TestMode)
	router := gin.New()
	RegisterAgentAPI(router)
	for _, authorized := range []bool{false, true} {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/inbounds/all", nil)
		req.TLS = &tls.ConnectionState{}
		if authorized {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		router.ServeHTTP(w, req)
		if !authorized {
			if w.Code != http.StatusUnauthorized {
				t.Fatalf("unprotected read: %d", w.Code)
			}
			continue
		}
		var response struct {
			Inbounds []model.Inbound `json:"inbounds"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if w.Code != http.StatusOK || len(response.Inbounds) != 2 {
			t.Fatalf("missing local inbounds: %s", w.Body.String())
		}
	}
}

func TestNodeInboundCopyFormBinding(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	var bound service.NodeInboundCopyRequest
	router.POST("/copy", func(c *gin.Context) {
		if err := c.ShouldBind(&bound); err != nil {
			t.Error(err)
		}
	})
	req := httptest.NewRequest(http.MethodPost, "/copy", strings.NewReader("sourceNodeId=1&targetNodeIds=2&targetNodeIds=3&ports=32101&ports=32102&overwrite=true"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	router.ServeHTTP(httptest.NewRecorder(), req)
	if bound.SourceNodeID != 1 || len(bound.TargetNodeIDs) != 2 || len(bound.Ports) != 2 || !bound.Overwrite {
		t.Fatalf("bad browser binding: %+v", bound)
	}
}
