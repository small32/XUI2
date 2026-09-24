package controller

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"xui/database"

	"github.com/gin-gonic/gin"
)

func TestAgentAPIRequiresTLSAndMachineToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	token := strings.Repeat("z", 64)
	t.Setenv("XUI_AGENT_TOKEN", token)
	router := gin.New()
	RegisterAgentAPI(router)
	tlsServer := httptest.NewTLSServer(router)
	defer tlsServer.Close()
	client := tlsServer.Client()
	request := func(auth string) int {
		req, _ := http.NewRequest("GET", tlsServer.URL+"/api/v1/capabilities", nil)
		if auth != "" {
			req.Header.Set("Authorization", auth)
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}
	if got := request(""); got != 401 {
		t.Fatalf("missing token: %d", got)
	}
	if got := request("Bearer wrong"); got != 401 {
		t.Fatalf("wrong token: %d", got)
	}
	if got := request("Bearer " + token); got != 200 {
		t.Fatalf("valid token: %d", got)
	}
	plain := httptest.NewServer(router)
	defer plain.Close()
	req, _ := http.NewRequest("GET", plain.URL+"/api/v1/capabilities", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("plain HTTP accepted: %d", resp.StatusCode)
	}
}

func TestAgentDisableMissingAccountIsNotAcknowledged(t *testing.T) {
	if err := database.InitDB(filepath.Join(t.TempDir(), "agent.db")); err != nil {
		t.Fatal(err)
	}
	token := strings.Repeat("q", 64)
	t.Setenv("XUI_AGENT_TOKEN", token)
	router := gin.New()
	RegisterAgentAPI(router)
	server := httptest.NewTLSServer(router)
	defer server.Close()
	request, err := http.NewRequest(http.MethodPost, server.URL+"/api/v1/inbounds/43339/disable", bytes.NewBufferString(`{"accountId":7,"reason":"limit","managerRevision":"`+strings.Repeat("a", 64)+`"}`))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("missing account returned %d, want 404", response.StatusCode)
	}
}
