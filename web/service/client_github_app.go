package service

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"xui/database"
	"xui/database/model"
)

const clientGitHubAppSettingKey = "clientGitHubApp"

// Credentials and pending handshakes live separately from the editable client settings.
// Only ClientGitHubAppStatus is returned to the browser.
type clientGitHubAppCredentials struct {
	AppID          int64  `json:"appId"`
	AppName        string `json:"appName"`
	Slug           string `json:"slug"`
	PrivateKey     string `json:"privateKey"`
	Repo           string `json:"repo"`
	InstallationID int64  `json:"installationId"`
}

type clientGitHubAppPending struct {
	AdminID   int                         `json:"adminId"`
	StateHash string                      `json:"stateHash"`
	Repo      string                      `json:"repo"`
	ExpiresAt int64                       `json:"expiresAt"`
	Stage     string                      `json:"stage"`
	Candidate *clientGitHubAppCredentials `json:"candidate,omitempty"`
}

type clientGitHubAppStore struct {
	Connection *clientGitHubAppCredentials `json:"connection,omitempty"`
	Pending    *clientGitHubAppPending     `json:"pending,omitempty"`
}

type ClientGitHubAppRegistration struct {
	Action   string `json:"action"`
	Manifest string `json:"manifest"`
}

type ClientGitHubAppStatus struct {
	Connected bool   `json:"connected"`
	Pending   bool   `json:"pending"`
	AppName   string `json:"appName"`
	Repo      string `json:"repo"`
}

var clientGitHubAppMu sync.Mutex
var clientGitHubAppTokenCache struct {
	key       string
	token     string
	expiresAt time.Time
}

func loadClientGitHubApp() (*clientGitHubAppStore, error) {
	var row model.Setting
	err := database.GetDB().Where("key = ?", clientGitHubAppSettingKey).First(&row).Error
	if database.IsNotFound(err) {
		return &clientGitHubAppStore{}, nil
	}
	if err != nil {
		return nil, err
	}
	var store clientGitHubAppStore
	if err := json.Unmarshal([]byte(row.Value), &store); err != nil {
		return nil, errors.New("GitHub 应用授权记录无效，请重新连接")
	}
	return &store, nil
}

func saveClientGitHubApp(store *clientGitHubAppStore) error {
	data, err := json.Marshal(store)
	if err != nil {
		return err
	}
	return setValue(database.GetDB(), clientGitHubAppSettingKey, string(data))
}

func (s *ClientService) BeginGitHubApp(ctx context.Context, adminID int, baseURL string) (*ClientGitHubAppRegistration, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if adminID <= 0 {
		return nil, errors.New("请使用管理员账号连接 GitHub")
	}
	base, err := ValidateClientURL(baseURL)
	if err != nil || base == "" {
		return nil, errors.New("管理端访问地址无效")
	}
	clientGitHubAppMu.Lock()
	defer clientGitHubAppMu.Unlock()
	cfg, err := s.Settings()
	if err != nil {
		return nil, err
	}
	repo, err := parseClientGitHubRepo(cfg.GitHubReleaseURL)
	if err != nil {
		return nil, err
	}
	state, err := clientRandom()
	if err != nil {
		return nil, err
	}
	name := "XUI Client " + state[:12]
	manifest, err := json.Marshal(map[string]interface{}{
		"name": name, "url": base,
		"hook_attributes": map[string]interface{}{"url": base + "xui/client/github/webhook", "active": false},
		"redirect_url":    base + "xui/client/github/callback",
		"setup_url":       base + "xui/client/github/installed?state=" + url.QueryEscape(state),
		"public":          false, "default_permissions": map[string]string{"contents": "read"},
		"default_events": []string{}, "request_oauth_on_install": false,
	})
	if err != nil {
		return nil, err
	}
	store, err := loadClientGitHubApp()
	if err != nil {
		return nil, err
	}
	store.Pending = &clientGitHubAppPending{AdminID: adminID, StateHash: clientHash(state), Repo: repo, ExpiresAt: time.Now().Add(time.Hour).Unix(), Stage: "register"}
	if err := saveClientGitHubApp(store); err != nil {
		return nil, err
	}
	return &ClientGitHubAppRegistration{Action: "https://github.com/settings/apps/new?state=" + url.QueryEscape(state), Manifest: string(manifest)}, nil
}

func (s *ClientService) validateGitHubAppPending(store *clientGitHubAppStore, adminID int, state, stage string) error {
	pending := store.Pending
	if pending == nil || adminID <= 0 || pending.AdminID != adminID || state == "" || pending.ExpiresAt <= time.Now().Unix() || pending.Stage != stage || subtle.ConstantTimeCompare([]byte(pending.StateHash), []byte(clientHash(state))) != 1 {
		return errors.New("GitHub 授权请求已失效或不属于当前管理员，请重新连接")
	}
	cfg, err := s.Settings()
	if err != nil {
		return err
	}
	repo, err := parseClientGitHubRepo(cfg.GitHubReleaseURL)
	if err != nil || !strings.EqualFold(repo, pending.Repo) {
		return errors.New("GitHub 仓库已改变，请重新连接")
	}
	return nil
}

func (s *ClientService) CompleteGitHubAppRegistration(ctx context.Context, adminID int, state, code string) (string, error) {
	return s.completeGitHubAppRegistration(ctx, adminID, state, code, newClientGitHubHTTPClient(false), "https://api.github.com")
}

func (s *ClientService) completeGitHubAppRegistration(ctx context.Context, adminID int, state, code string, client *http.Client, apiBase string) (string, error) {
	clientGitHubAppMu.Lock()
	defer clientGitHubAppMu.Unlock()
	store, err := loadClientGitHubApp()
	if err != nil {
		return "", err
	}
	if err := s.validateGitHubAppPending(store, adminID, state, "register"); err != nil {
		return "", err
	}
	if code == "" || len(code) > 256 {
		return "", errors.New("GitHub 授权代码无效，请重新连接")
	}
	// Persist the claim before using the one-time code, including across restarts.
	store.Pending.Stage = "converting"
	if err := saveClientGitHubApp(store); err != nil {
		return "", err
	}
	var app struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
		Slug string `json:"slug"`
		PEM  string `json:"pem"`
	}
	if err := clientGitHubAppJSON(ctx, client, http.MethodPost, apiBase+"/app-manifests/"+url.PathEscape(code)+"/conversions", "", nil, &app); err != nil {
		return "", err
	}
	if app.ID <= 0 || app.Slug == "" || strings.ContainsAny(app.Slug, "/?#\\") || !clientGitHubRepoPart.MatchString(app.Slug) {
		return "", errors.New("GitHub 返回的应用信息无效，请重新连接")
	}
	if _, err := parseClientGitHubAppKey(app.PEM); err != nil {
		return "", err
	}
	store.Pending.Candidate = &clientGitHubAppCredentials{AppID: app.ID, AppName: app.Name, Slug: app.Slug, PrivateKey: app.PEM, Repo: store.Pending.Repo}
	store.Pending.Stage = "install"
	if err := saveClientGitHubApp(store); err != nil {
		return "", err
	}
	return "https://github.com/apps/" + app.Slug + "/installations/new?state=" + url.QueryEscape(state), nil
}

func (s *ClientService) CompleteGitHubAppInstallation(ctx context.Context, adminID int, state string) error {
	return s.completeGitHubAppInstallation(ctx, adminID, state, newClientGitHubHTTPClient(false), "https://api.github.com")
}

func (s *ClientService) completeGitHubAppInstallation(ctx context.Context, adminID int, state string, client *http.Client, apiBase string) error {
	clientGitHubAppMu.Lock()
	defer clientGitHubAppMu.Unlock()
	store, err := loadClientGitHubApp()
	if err != nil {
		return err
	}
	if err := s.validateGitHubAppPending(store, adminID, state, "install"); err != nil {
		return err
	}
	if store.Pending.Candidate == nil {
		return errors.New("GitHub 应用信息缺失，请重新连接")
	}
	candidate := *store.Pending.Candidate
	jwt, err := clientGitHubAppJWT(&candidate, time.Now())
	if err != nil {
		return err
	}
	var installation struct {
		ID          int64             `json:"id"`
		AppID       int64             `json:"app_id"`
		Permissions map[string]string `json:"permissions"`
		SuspendedAt *string           `json:"suspended_at"`
	}
	if err := clientGitHubAppJSON(ctx, client, http.MethodGet, apiBase+"/repos/"+candidate.Repo+"/installation", jwt, nil, &installation); err != nil {
		return err
	}
	if installation.ID <= 0 || installation.AppID != candidate.AppID || installation.SuspendedAt != nil || (installation.Permissions["contents"] != "read" && installation.Permissions["contents"] != "write") {
		return errors.New("GitHub 应用未获得目标仓库的 Contents 读取权限，请选择正确仓库安装")
	}
	candidate.InstallationID = installation.ID
	token, expiresAt, err := mintClientGitHubAppToken(ctx, client, apiBase, &candidate)
	if err != nil {
		return err
	}
	// A failed reauthorization never replaces the working connection.
	store.Connection, store.Pending = &candidate, nil
	if err := saveClientGitHubApp(store); err != nil {
		return err
	}
	cacheClientGitHubAppToken(&candidate, token, expiresAt)
	return nil
}

func (s *ClientService) GitHubAppStatus() (*ClientGitHubAppStatus, error) {
	clientGitHubAppMu.Lock()
	defer clientGitHubAppMu.Unlock()
	store, err := loadClientGitHubApp()
	if err != nil {
		return nil, err
	}
	cfg, err := s.Settings()
	if err != nil {
		return nil, err
	}
	repo, _ := parseClientGitHubRepo(cfg.GitHubReleaseURL)
	status := &ClientGitHubAppStatus{Repo: repo}
	if connection := store.Connection; connection != nil {
		status.AppName, status.Repo = connection.AppName, connection.Repo
		status.Connected = connection.InstallationID > 0 && connection.PrivateKey != "" && strings.EqualFold(connection.Repo, repo)
	}
	if pending := store.Pending; pending != nil && pending.ExpiresAt > time.Now().Unix() {
		status.Pending = true
		if store.Connection == nil {
			status.Repo = pending.Repo
			if pending.Candidate != nil {
				status.AppName = pending.Candidate.AppName
			}
		}
	}
	return status, nil
}

func (s *ClientService) DisconnectGitHubApp() error {
	clientGitHubAppMu.Lock()
	defer clientGitHubAppMu.Unlock()
	if err := database.GetDB().Where("key = ?", clientGitHubAppSettingKey).Delete(&model.Setting{}).Error; err != nil {
		return err
	}
	clientGitHubAppTokenCache.key, clientGitHubAppTokenCache.token = "", ""
	clientGitHubAppTokenCache.expiresAt = time.Time{}
	return nil
}

func (s *ClientService) GitHubAccessToken(ctx context.Context, cfg *ClientSettings) (string, error) {
	return s.gitHubAccessToken(ctx, cfg, newClientGitHubHTTPClient(false), "https://api.github.com")
}

// Release metadata uses a stable identity rather than the hourly installation token.
func (s *ClientService) GitHubCredentialKey(cfg *ClientSettings) (string, error) {
	if cfg == nil {
		var err error
		cfg, err = s.Settings()
		if err != nil {
			return "", err
		}
	}
	clientGitHubAppMu.Lock()
	defer clientGitHubAppMu.Unlock()
	store, err := loadClientGitHubApp()
	if err != nil {
		return "", err
	}
	repo, err := parseClientGitHubRepo(cfg.GitHubReleaseURL)
	if connection := store.Connection; err == nil && connection != nil && strings.EqualFold(repo, connection.Repo) {
		return fmt.Sprintf("app:%d:%d:%s", connection.AppID, connection.InstallationID, clientHash(connection.PrivateKey)), nil
	}
	return clientHash(cfg.GitHubToken), nil
}

func (s *ClientService) gitHubAccessToken(ctx context.Context, cfg *ClientSettings, client *http.Client, apiBase string) (string, error) {
	if cfg == nil {
		var err error
		cfg, err = s.Settings()
		if err != nil {
			return "", err
		}
	}
	clientGitHubAppMu.Lock()
	defer clientGitHubAppMu.Unlock()
	store, err := loadClientGitHubApp()
	if err != nil {
		return "", err
	}
	repo, err := parseClientGitHubRepo(cfg.GitHubReleaseURL)
	if err != nil || store.Connection == nil || !strings.EqualFold(repo, store.Connection.Repo) {
		return cfg.GitHubToken, nil
	}
	connection := store.Connection
	if clientGitHubAppTokenCache.key == clientGitHubAppCacheKey(connection) && time.Now().Add(time.Minute).Before(clientGitHubAppTokenCache.expiresAt) {
		return clientGitHubAppTokenCache.token, nil
	}
	token, expiresAt, err := mintClientGitHubAppToken(ctx, client, apiBase, connection)
	if err != nil {
		return "", err
	}
	cacheClientGitHubAppToken(connection, token, expiresAt)
	return token, nil
}

func clientGitHubAppCacheKey(connection *clientGitHubAppCredentials) string {
	return fmt.Sprintf("%d:%d:%s:%s", connection.AppID, connection.InstallationID, strings.ToLower(connection.Repo), clientHash(connection.PrivateKey))
}

func cacheClientGitHubAppToken(connection *clientGitHubAppCredentials, token string, expiresAt time.Time) {
	clientGitHubAppTokenCache.key = clientGitHubAppCacheKey(connection)
	clientGitHubAppTokenCache.token, clientGitHubAppTokenCache.expiresAt = token, expiresAt
}

func parseClientGitHubAppKey(raw string) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode([]byte(raw))
	if block == nil {
		return nil, errors.New("GitHub 应用私钥无效，请重新连接")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		if rsaKey, ok := key.(*rsa.PrivateKey); ok {
			return rsaKey, nil
		}
	}
	return nil, errors.New("GitHub 应用私钥无效，请重新连接")
}

func clientGitHubAppJWT(connection *clientGitHubAppCredentials, now time.Time) (string, error) {
	key, err := parseClientGitHubAppKey(connection.PrivateKey)
	if err != nil {
		return "", err
	}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	claims, err := json.Marshal(map[string]interface{}{"iat": now.Add(-time.Minute).Unix(), "exp": now.Add(9 * time.Minute).Unix(), "iss": strconv.FormatInt(connection.AppID, 10)})
	if err != nil {
		return "", err
	}
	unsigned := header + "." + base64.RawURLEncoding.EncodeToString(claims)
	digest := sha256.Sum256([]byte(unsigned))
	signature, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, digest[:])
	if err != nil {
		return "", errors.New("GitHub 应用签名失败，请重新连接")
	}
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(signature), nil
}

func mintClientGitHubAppToken(ctx context.Context, client *http.Client, apiBase string, connection *clientGitHubAppCredentials) (string, time.Time, error) {
	jwt, err := clientGitHubAppJWT(connection, time.Now())
	if err != nil {
		return "", time.Time{}, err
	}
	parts := strings.SplitN(connection.Repo, "/", 2)
	if len(parts) != 2 || connection.InstallationID <= 0 {
		return "", time.Time{}, errors.New("GitHub 应用安装信息无效，请重新连接")
	}
	var result struct {
		Token     string    `json:"token"`
		ExpiresAt time.Time `json:"expires_at"`
	}
	body := map[string]interface{}{"repositories": []string{parts[1]}, "permissions": map[string]string{"contents": "read"}}
	endpoint := fmt.Sprintf("%s/app/installations/%d/access_tokens", apiBase, connection.InstallationID)
	if err := clientGitHubAppJSON(ctx, client, http.MethodPost, endpoint, jwt, body, &result); err != nil {
		return "", time.Time{}, err
	}
	if result.Token == "" || !result.ExpiresAt.After(time.Now().Add(time.Minute)) {
		return "", time.Time{}, errors.New("GitHub 返回的安装令牌无效，请重新连接")
	}
	return result.Token, result.ExpiresAt, nil
}

func clientGitHubAppJSON(ctx context.Context, client *http.Client, method, endpoint, token string, body, result interface{}) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return errors.New("GitHub 授权请求地址无效")
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "XUI2-client-app")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := client.Do(req)
	if err != nil {
		return errors.New("连接 GitHub 授权服务失败，请检查网络或本机代理")
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("GitHub 授权失败 (HTTP %d)，请检查应用安装及仓库权限", response.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 2*1024*1024)).Decode(result); err != nil {
		return errors.New("GitHub 授权响应无效，请重新连接")
	}
	return nil
}
