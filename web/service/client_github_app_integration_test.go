package service

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// Exercise the real release, direct-link and relay entry points with App auth.
// The TLS server replaces only GitHub's network transport; no live credentials
// or GitHub mutations are used.
func TestClientGitHubAppReleaseAndDownloadsRenewAutomatically(t *testing.T) {
	clientTestDB(t)
	s := new(ClientService)
	if err := s.SaveSettings(&ClientSettings{Enabled: true, GitHubReleaseEnable: true, GitHubReleaseURL: "https://github.com/small32/SClash/releases/"}); err != nil {
		t.Fatal(err)
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	privateKey := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	connection := &clientGitHubAppCredentials{AppID: 21, AppName: "XUI Test", Slug: "xui-test", PrivateKey: privateKey, Repo: "small32/SClash", InstallationID: 42}
	if err := saveClientGitHubApp(&clientGitHubAppStore{Connection: connection}); err != nil {
		t.Fatal(err)
	}
	var tokens, metadata int32
	var failMint int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/app/installations/42/access_tokens" {
			if atomic.LoadInt32(&failMint) != 0 {
				w.WriteHeader(503)
				return
			}
			if r.Method != http.MethodPost || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer eyJ") {
				t.Error("installation token request must use an App JWT")
			}
			n := atomic.AddInt32(&tokens, 1)
			json.NewEncoder(w).Encode(map[string]interface{}{"token": fmt.Sprintf("ghs_mock_%d", n), "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
			return
		}
		if r.Header.Get("Authorization") != fmt.Sprintf("Bearer ghs_mock_%d", atomic.LoadInt32(&tokens)) {
			t.Error("release request did not use the current installation token")
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/repos/small32/SClash/releases/latest":
			atomic.AddInt32(&metadata, 1)
			fmt.Fprint(w, `{"tag_name":"v1","assets":[{"id":7,"name":"SClash-windows-x64.exe","size":9}]}`)
		case "/repos/small32/SClash/releases/assets/7":
			if r.Header.Get("Range") != "" {
				if r.Header.Get("Range") != "bytes=0-3" {
					t.Error("relay range was not preserved")
				}
				w.Header().Set("Content-Type", "application/octet-stream")
				w.Header().Set("Content-Range", "bytes 0-3/9")
				w.WriteHeader(206)
				fmt.Fprint(w, "inst")
				return
			}
			w.Header().Set("Location", "https://release-assets.githubusercontent.com/SClash.exe?signed=temporary")
			w.WriteHeader(302)
		default:
			t.Errorf("unexpected GitHub request: %s", r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	transport := server.Client().Transport.(*http.Transport).Clone()
	transport.TLSClientConfig.ServerName = server.Certificate().DNSNames[0]
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}
	oldTransport := http.DefaultTransport
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = oldTransport; transport.CloseIdleConnections() })
	t.Cleanup(func() { s.DisconnectGitHubApp() })
	ctx := context.Background()
	release, err := s.GitHubRelease(ctx, true)
	if err != nil || release.TagName != "v1" || atomic.LoadInt32(&tokens) != 1 {
		t.Fatalf("initial private release: %+v %v", release, err)
	}
	link, err := s.DirectGitHubAssetURL(ctx, 7)
	if err != nil || !strings.HasPrefix(link, "https://release-assets.githubusercontent.com/") || strings.Contains(link, "ghs_mock") {
		t.Fatalf("private direct download: %q %v", link, err)
	}
	clientGitHubAppMu.Lock()
	clientGitHubAppTokenCache.expiresAt = time.Now().Add(-time.Hour)
	clientGitHubAppMu.Unlock()
	response, _, err := s.OpenGitHubAsset(ctx, 7, "bytes=0-3")
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || response.StatusCode != 206 || string(body) != "inst" || atomic.LoadInt32(&tokens) != 2 || atomic.LoadInt32(&metadata) != 1 {
		t.Fatalf("automatic relay renewal: status=%d body=%q tokens=%d metadata=%d err=%v", response.StatusCode, body, tokens, metadata, err)
	}
	// Empty the process cache to simulate restart, keeping only persisted App data.
	clientGitHubAppMu.Lock()
	clientGitHubAppTokenCache.key = ""
	clientGitHubAppMu.Unlock()
	if _, err := new(ClientService).GitHubRelease(ctx, true); err != nil || atomic.LoadInt32(&tokens) != 3 {
		t.Fatalf("restart did not recover from saved App credentials: %v", err)
	}
	atomic.StoreInt32(&failMint, 1)
	clientGitHubAppMu.Lock()
	clientGitHubAppTokenCache.expiresAt = time.Now().Add(-time.Hour)
	clientGitHubAppMu.Unlock()
	stale, err := s.GitHubRelease(ctx, true)
	if err != nil || stale.TagName != "v1" || stale.Error == "" || atomic.LoadInt32(&metadata) != 2 {
		t.Fatalf("renewal failure discarded last successful release: %+v %v", stale, err)
	}
}
