package controller

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"xui/database"
	"xui/database/model"
	"xui/web/service"
	"xui/web/session"
)

func clientGuideTestRouter(t *testing.T, login func(*gin.Context)) *gin.Engine {
	t.Helper()
	engine := clientGitHubAppTestRouter(t, login)
	RegisterClientAPI(engine.Group("/panel/"))
	return engine
}

func clientGuideTestRequest(engine *gin.Engine, method, path, body, token string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(method, "https://panel.example"+path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Requested-With", "XMLHttpRequest")
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	engine.ServeHTTP(w, r)
	return w
}

func TestClientGuideAdminWriteAndAuthenticatedRead(t *testing.T) {
	admin := clientGitHubAppTestDB(t)
	adminRouter := clientGuideTestRouter(t, func(c *gin.Context) {
		if err := session.SetLoginUser(c, admin); err != nil {
			t.Fatal(err)
		}
	})
	in := &model.Inbound{Port: 18888, Enable: true, Protocol: model.Shadowsocks, Settings: `{"method":"aes-128-gcm","password":"guide-password"}`, StreamSettings: `{}`, Sniffing: `{}`, Tag: "inbound-18888"}
	if err := database.GetDB().Create(in).Error; err != nil {
		t.Fatal(err)
	}
	restrictedRouter := clientGuideTestRouter(t, func(c *gin.Context) {
		if err := session.SetRestrictedLogin(c, in.Id, "guide-password"); err != nil {
			t.Fatal(err)
		}
	})
	for _, content := range []string{"<p>客户端使用说明 v1</p>", "<p>客户端使用说明 v2</p>"} {
		body, _ := json.Marshal(map[string]string{"html": content})
		w := clientGuideTestRequest(adminRouter, http.MethodPost, "/panel/xui/client/guide/update", string(body), "")
		var saved struct {
			Success bool                `json:"success"`
			Obj     service.ClientGuide `json:"obj"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &saved); err != nil || !saved.Success || saved.Obj.HTML == "" || saved.Obj.UpdatedAt <= 0 {
			t.Fatalf("administrator save failed: %d %s", w.Code, w.Body.String())
		}
		for _, engine := range []*gin.Engine{adminRouter, restrictedRouter} {
			w = clientGuideTestRequest(engine, http.MethodGet, "/panel/api/client/v1/guide", "", "")
			var got service.ClientGuide
			if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &got) != nil || got != saved.Obj {
				t.Fatalf("saved guide was not immediately visible to logged-in user: %d %s", w.Code, w.Body.String())
			}
			if w.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("guide response may be cached")
			}
		}
		w = clientGuideTestRequest(adminRouter, http.MethodPost, "/panel/xui/client/guide", "{}", "")
		if !strings.Contains(w.Body.String(), `"success":true`) || w.Header().Get("Cache-Control") != "no-store" {
			t.Fatalf("administrator guide query failed: %s", w.Body.String())
		}
	}
	for _, path := range []string{"guide", "guide/update"} {
		w := clientGuideTestRequest(restrictedRouter, http.MethodPost, "/panel/xui/client/"+path, `{"html":"overwritten"}`, "")
		if !strings.Contains(w.Body.String(), `"success":false`) {
			t.Fatalf("restricted account reached administrator guide endpoint %s: %s", path, w.Body.String())
		}
	}
	anonymous := clientGuideTestRouter(t, nil)
	w := clientGuideTestRequest(anonymous, http.MethodGet, "/panel/api/client/v1/guide", "", "")
	if strings.Contains(w.Body.String(), "客户端使用说明") || !strings.Contains(w.Body.String(), `"success":false`) {
		t.Fatalf("anonymous guide read allowed: %d %s", w.Code, w.Body.String())
	}
	tokens, err := new(service.ClientService).Login("18888", "guide-password")
	if err != nil {
		t.Fatal(err)
	}
	w = clientGuideTestRequest(anonymous, http.MethodGet, "/panel/api/client/v1/guide", "", tokens.AccessToken)
	var guide service.ClientGuide
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &guide) != nil || !strings.Contains(guide.HTML, "v2") {
		t.Fatalf("client Bearer guide read failed: %d %s", w.Code, w.Body.String())
	}
	if w := clientGuideTestRequest(anonymous, http.MethodGet, "/api/client/v1/guide", "", tokens.AccessToken); w.Code != http.StatusNotFound {
		t.Fatal("guide API ignored panel path prefix")
	}
	t.Setenv("XUI_ROLE", "agent")
	agent := gin.New()
	RegisterClientAPI(agent.Group("/panel/"))
	RegisterClientAdmin(agent.Group("/panel/xui"))
	if len(agent.Routes()) != 0 {
		t.Fatal("agent registered manager guide routes")
	}
}

func TestClientGuideRejectsInvalidAndOversizedRequests(t *testing.T) {
	admin := clientGitHubAppTestDB(t)
	engine := clientGuideTestRouter(t, func(c *gin.Context) {
		if err := session.SetLoginUser(c, admin); err != nil {
			t.Fatal(err)
		}
	})
	for _, body := range []string{`{"html":123}`, `{"html":null}`, `{"html":{}}`, `{}`} {
		w := clientGuideTestRequest(engine, http.MethodPost, "/panel/xui/client/guide/update", body, "")
		if !strings.Contains(w.Body.String(), `"success":false`) {
			t.Fatalf("non-string HTML accepted: %s: %s", body, w.Body.String())
		}
	}
	body := `{"html":"` + strings.Repeat("x", int(service.ClientGuideMaxSize)+1) + `"}`
	w := clientGuideTestRequest(engine, http.MethodPost, "/panel/xui/client/guide/update", body, "")
	if !strings.Contains(w.Body.String(), `"success":false`) {
		t.Fatal("HTML over the service size limit was accepted")
	}
	r := httptest.NewRequest(http.MethodPost, "https://panel.example/panel/xui/client/guide/update", strings.NewReader(`{"html":"<p>small</p>"}`))
	r.Header.Set("Content-Type", "application/json")
	r.ContentLength = 6*int64(service.ClientGuideMaxSize) + 1024*1024 + 1
	w = httptest.NewRecorder()
	engine.ServeHTTP(w, r)
	if !strings.Contains(w.Body.String(), `"success":false`) {
		t.Fatal("request above the JSON body size limit was accepted")
	}
}
