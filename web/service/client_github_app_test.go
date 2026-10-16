package service

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"xui/database"
	"xui/database/model"
)

func githubAppTestPrivateKey(t *testing.T) (*rsa.PrivateKey, string) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return key, string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
}

func githubAppTestState(t *testing.T, registration *ClientGitHubAppRegistration) string {
	t.Helper()
	action, err := url.Parse(registration.Action)
	if err != nil || action.Host != "github.com" || action.Path != "/settings/apps/new" {
		t.Fatal("invalid registration action")
	}
	state := action.Query().Get("state")
	if len(state) != 64 {
		t.Fatal("missing registration state")
	}
	return state
}

func githubAppTestVerifyJWT(t *testing.T, request *http.Request, key *rsa.PrivateKey) {
	t.Helper()
	parts := strings.Split(strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer "), ".")
	if len(parts) != 3 {
		t.Error("missing app JWT")
		return
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Error("invalid JWT signature encoding")
		return
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, digest[:], signature); err != nil {
		t.Error("app JWT signature did not verify")
	}
	claimsBytes, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		t.Error("invalid JWT claims encoding")
		return
	}
	var claims struct {
		Issuer string `json:"iss"`
		Issued int64  `json:"iat"`
		Expiry int64  `json:"exp"`
	}
	if json.Unmarshal(claimsBytes, &claims) != nil || claims.Issuer != "123" || claims.Issued > time.Now().Unix() || claims.Expiry <= time.Now().Unix() || claims.Expiry > time.Now().Add(10*time.Minute).Unix() {
		t.Error("invalid JWT issuer or lifetime")
	}
}

func TestClientGitHubAppManifestRegistrationAndInstallation(t *testing.T) {
	clientTestDB(t)
	key, keyPEM := githubAppTestPrivateKey(t)
	var conversions, mints int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/app-manifests/test-code/conversions":
			atomic.AddInt32(&conversions, 1)
			if r.Method != http.MethodPost || r.Header.Get("Authorization") != "" {
				t.Error("manifest conversion should not require a PAT")
			}
			json.NewEncoder(w).Encode(map[string]interface{}{"id": 123, "name": "XUI Test", "slug": "xui-test", "pem": keyPEM})
		case "/repos/small32/SClash/installation":
			githubAppTestVerifyJWT(t, r, key)
			fmt.Fprint(w, `{"id":456,"app_id":123,"permissions":{"contents":"read"},"suspended_at":null}`)
		case "/app/installations/456/access_tokens":
			githubAppTestVerifyJWT(t, r, key)
			if r.Method != http.MethodPost {
				t.Error("token request must be POST")
			}
			var body struct {
				Repositories []string          `json:"repositories"`
				Permissions  map[string]string `json:"permissions"`
			}
			if json.NewDecoder(r.Body).Decode(&body) != nil || len(body.Repositories) != 1 || body.Repositories[0] != "SClash" || body.Permissions["contents"] != "read" {
				t.Error("installation token must be limited to the selected repository and read-only contents")
			}
			count := atomic.AddInt32(&mints, 1)
			json.NewEncoder(w).Encode(map[string]interface{}{"token": fmt.Sprintf("installation-secret-%d", count), "expires_at": time.Now().Add(time.Hour).UTC()})
		default:
			t.Errorf("unexpected GitHub request: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	s := new(ClientService)
	cfg := &ClientSettings{Enabled: true, GitHubReleaseEnable: true, GitHubReleaseURL: "https://github.com/small32/SClash/releases/"}
	if err := s.SaveSettings(cfg); err != nil {
		t.Fatal(err)
	}
	registration, err := s.BeginGitHubApp(context.Background(), 7, "https://panel.example/prefix/")
	if err != nil {
		t.Fatal(err)
	}
	state := githubAppTestState(t, registration)
	var manifest struct {
		Name        string            `json:"name"`
		RedirectURL string            `json:"redirect_url"`
		SetupURL    string            `json:"setup_url"`
		Public      bool              `json:"public"`
		Permissions map[string]string `json:"default_permissions"`
		Hook        struct {
			Active bool `json:"active"`
		} `json:"hook_attributes"`
	}
	if json.Unmarshal([]byte(registration.Manifest), &manifest) != nil || manifest.Name == "" || manifest.Public || manifest.Hook.Active || manifest.Permissions["contents"] != "read" || manifest.RedirectURL != "https://panel.example/prefix/xui/client/github/callback" || manifest.SetupURL != "https://panel.example/prefix/xui/client/github/installed?state="+state {
		t.Fatal("manifest does not preserve the panel prefix or least-privilege setup")
	}
	for _, attempt := range []struct {
		admin int
		state string
	}{{8, state}, {7, "wrong"}, {7, ""}} {
		if _, err := s.completeGitHubAppRegistration(context.Background(), attempt.admin, attempt.state, "test-code", server.Client(), server.URL); err == nil {
			t.Fatal("invalid authorization state accepted")
		}
	}
	cfg.GitHubReleaseURL = "https://github.com/small32/Other/releases/"
	if err := s.SaveSettings(cfg); err != nil {
		t.Fatal(err)
	}
	if _, err := s.completeGitHubAppRegistration(context.Background(), 7, state, "test-code", server.Client(), server.URL); err == nil {
		t.Fatal("changed repository accepted during registration")
	}
	if atomic.LoadInt32(&conversions) != 0 {
		t.Fatal("invalid state triggered a GitHub write")
	}
	cfg.GitHubReleaseURL = "https://github.com/small32/SClash/releases/"
	if err := s.SaveSettings(cfg); err != nil {
		t.Fatal(err)
	}
	installURL, err := s.completeGitHubAppRegistration(context.Background(), 7, state, "test-code", server.Client(), server.URL)
	if err != nil || installURL != "https://github.com/apps/xui-test/installations/new?state="+state {
		t.Fatalf("installation link: %v", err)
	}
	if _, err := s.completeGitHubAppRegistration(context.Background(), 7, state, "test-code", server.Client(), server.URL); err == nil || atomic.LoadInt32(&conversions) != 1 {
		t.Fatal("manifest callback replay accepted")
	}
	// A new service instance completes the handshake from persisted pending data.
	restarted := new(ClientService)
	if err := restarted.completeGitHubAppInstallation(context.Background(), 7, state, server.Client(), server.URL); err != nil {
		t.Fatal(err)
	}
	if err := restarted.completeGitHubAppInstallation(context.Background(), 7, state, server.Client(), server.URL); err == nil {
		t.Fatal("installation callback replay accepted")
	}
	status, err := restarted.GitHubAppStatus()
	if err != nil || !status.Connected || status.Pending || status.AppName != "XUI Test" || status.Repo != "small32/SClash" {
		t.Fatalf("unexpected connected status: %+v %v", status, err)
	}
	statusJSON, _ := json.Marshal(status)
	if strings.Contains(string(statusJSON), "PRIVATE KEY") || strings.Contains(string(statusJSON), "installation-secret") || strings.Contains(string(statusJSON), state) {
		t.Fatal("authorization status leaked secrets")
	}
	stableKey, err := restarted.GitHubCredentialKey(cfg)
	if err != nil || !strings.HasPrefix(stableKey, "app:123:456:") {
		t.Fatal("missing stable app cache identity")
	}
	token, err := restarted.gitHubAccessToken(context.Background(), cfg, server.Client(), server.URL)
	if err != nil || token != "installation-secret-1" || atomic.LoadInt32(&mints) != 1 {
		t.Fatal("installation token cache did not survive repeated requests")
	}
	clientGitHubAppMu.Lock()
	clientGitHubAppTokenCache.key, clientGitHubAppTokenCache.token = "", ""
	clientGitHubAppTokenCache.expiresAt = time.Time{}
	clientGitHubAppMu.Unlock()
	token, err = new(ClientService).gitHubAccessToken(context.Background(), cfg, server.Client(), server.URL)
	if err != nil || token != "installation-secret-2" || atomic.LoadInt32(&mints) != 2 {
		t.Fatal("persisted credentials did not restore authorization after a restart")
	}
	if afterRestart, _ := restarted.GitHubCredentialKey(cfg); afterRestart != stableKey {
		t.Fatal("hourly token rotation changed the release cache identity")
	}
	var stored model.Setting
	if err := database.GetDB().Where("key = ?", clientGitHubAppSettingKey).First(&stored).Error; err != nil || !strings.Contains(stored.Value, "PRIVATE KEY") || strings.Contains(stored.Value, "installation-secret") || strings.Contains(stored.Value, state) {
		t.Fatal("persistent authorization must save the key, but not installation tokens or plaintext state")
	}
	if err := restarted.DisconnectGitHubApp(); err != nil {
		t.Fatal(err)
	}
	status, _ = restarted.GitHubAppStatus()
	if status.Connected || status.Pending || clientGitHubAppTokenCache.token != "" {
		t.Fatal("disconnect retained app authorization")
	}
	cfg.GitHubToken = "manual-fallback"
	if token, err = restarted.gitHubAccessToken(context.Background(), cfg, server.Client(), server.URL); err != nil || token != cfg.GitHubToken {
		t.Fatal("manual token fallback stopped working")
	}
}

func TestClientGitHubAppExpiredStateAndFailedReauthorizationPreserveConnection(t *testing.T) {
	clientTestDB(t)
	_, keyPEM := githubAppTestPrivateKey(t)
	s := new(ClientService)
	cfg := &ClientSettings{Enabled: true, GitHubReleaseURL: "https://github.com/small32/SClash/releases/"}
	if err := s.SaveSettings(cfg); err != nil {
		t.Fatal(err)
	}
	old := &clientGitHubAppCredentials{AppID: 99, AppName: "Existing App", Slug: "existing-app", PrivateKey: keyPEM, Repo: "small32/SClash", InstallationID: 77}
	if err := saveClientGitHubApp(&clientGitHubAppStore{Connection: old}); err != nil {
		t.Fatal(err)
	}
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		if strings.HasPrefix(r.URL.Path, "/app-manifests/") {
			json.NewEncoder(w).Encode(map[string]interface{}{"id": 123, "name": "Replacement", "slug": "replacement", "pem": keyPEM})
			return
		}
		fmt.Fprint(w, `{"id":456,"app_id":999,"permissions":{"contents":"read"}}`)
	}))
	defer server.Close()
	registration, err := s.BeginGitHubApp(context.Background(), 7, "https://panel.example/")
	if err != nil {
		t.Fatal(err)
	}
	state := githubAppTestState(t, registration)
	store, _ := loadClientGitHubApp()
	store.Pending.ExpiresAt = time.Now().Add(-time.Second).Unix()
	if err := saveClientGitHubApp(store); err != nil {
		t.Fatal(err)
	}
	if _, err := s.completeGitHubAppRegistration(context.Background(), 7, state, "test-code", server.Client(), server.URL); err == nil || atomic.LoadInt32(&calls) != 0 {
		t.Fatal("expired state reached the conversion API")
	}
	registration, err = s.BeginGitHubApp(context.Background(), 7, "https://panel.example/")
	if err != nil {
		t.Fatal(err)
	}
	state = githubAppTestState(t, registration)
	if _, err := s.completeGitHubAppRegistration(context.Background(), 7, state, "test-code", server.Client(), server.URL); err != nil {
		t.Fatal(err)
	}
	if err := s.completeGitHubAppInstallation(context.Background(), 7, state, server.Client(), server.URL); err == nil {
		t.Fatal("installation belonging to a different app was accepted")
	}
	store, _ = loadClientGitHubApp()
	if store.Connection == nil || store.Connection.AppID != 99 || store.Connection.InstallationID != 77 {
		t.Fatal("failed reauthorization replaced the working connection")
	}
	cfg.GitHubReleaseURL = "https://github.com/small32/Other/releases/"
	if err := s.SaveSettings(cfg); err != nil {
		t.Fatal(err)
	}
	before := atomic.LoadInt32(&calls)
	if err := s.completeGitHubAppInstallation(context.Background(), 7, state, server.Client(), server.URL); err == nil || atomic.LoadInt32(&calls) != before {
		t.Fatal("changed repository reached the installation API")
	}
	status, err := s.GitHubAppStatus()
	if err != nil || status.Connected {
		t.Fatal("authorization for a different repository was reported connected")
	}
	cfg.GitHubToken = "other-repository-token"
	if token, err := s.gitHubAccessToken(context.Background(), cfg, server.Client(), server.URL); err != nil || token != cfg.GitHubToken {
		t.Fatal("different repository used the app credential")
	}
}

func TestClientGitHubAppTokenRefreshIsSingleFlight(t *testing.T) {
	clientTestDB(t)
	key, keyPEM := githubAppTestPrivateKey(t)
	s := new(ClientService)
	cfg := &ClientSettings{Enabled: true, GitHubReleaseURL: "https://github.com/small32/SClash/releases/"}
	if err := s.SaveSettings(cfg); err != nil {
		t.Fatal(err)
	}
	connection := &clientGitHubAppCredentials{AppID: 123, PrivateKey: keyPEM, Repo: "small32/SClash", InstallationID: 456}
	if err := saveClientGitHubApp(&clientGitHubAppStore{Connection: connection}); err != nil {
		t.Fatal(err)
	}
	clientGitHubAppMu.Lock()
	cacheClientGitHubAppToken(connection, "almost-expired", time.Now().Add(30*time.Second))
	clientGitHubAppMu.Unlock()
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		githubAppTestVerifyJWT(t, r, key)
		json.NewEncoder(w).Encode(map[string]interface{}{"token": "renewed-secret", "expires_at": time.Now().Add(time.Hour).UTC()})
	}))
	defer server.Close()
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			token, err := s.gitHubAccessToken(context.Background(), cfg, server.Client(), server.URL)
			if err != nil || token != "renewed-secret" {
				t.Errorf("concurrent refresh failed: %v", err)
			}
		}()
	}
	wg.Wait()
	if atomic.LoadInt32(&calls) != 1 {
		t.Fatal("concurrent requests minted multiple installation tokens")
	}
	clientGitHubAppMu.Lock()
	clientGitHubAppTokenCache.expiresAt = time.Now().Add(-time.Second)
	clientGitHubAppMu.Unlock()
	if _, err := s.gitHubAccessToken(context.Background(), cfg, server.Client(), server.URL); err != nil || atomic.LoadInt32(&calls) != 2 {
		t.Fatal("expired installation token was not replaced")
	}
}

func TestClientGitHubAppPKCS8KeyAndSafeErrors(t *testing.T) {
	key, keyPEM := githubAppTestPrivateKey(t)
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	pkcs8 := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	if _, err := clientGitHubAppJWT(&clientGitHubAppCredentials{AppID: 123, PrivateKey: pkcs8}, time.Now()); err != nil {
		t.Fatal("PKCS8 RSA key rejected")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, keyPEM+" secret-token")
	}))
	defer server.Close()
	var result map[string]interface{}
	err = clientGitHubAppJSON(context.Background(), server.Client(), http.MethodGet, server.URL, "secret-token", nil, &result)
	if err == nil || strings.Contains(err.Error(), "PRIVATE KEY") || strings.Contains(err.Error(), "secret-token") {
		t.Fatal("upstream error leaked secret response data")
	}
}
