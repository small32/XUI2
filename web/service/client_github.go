package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"strings"
	"sync"
	"time"
	"xui/xray"
)

const clientGitHubCacheTTL = 10 * time.Minute

var clientGitHubRepoPart = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

func parseClientGitHubRepo(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Host, "github.com") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return "", errors.New("Release 地址须为 https://github.com/所有者/仓库/releases/")
		}
		raw = strings.Trim(u.Path, "/")
	}
	parts := strings.Split(strings.Trim(raw, "/"), "/")
	if len(parts) < 2 || !clientGitHubRepoPart.MatchString(parts[0]) || !clientGitHubRepoPart.MatchString(parts[1]) || parts[0] == "." || parts[0] == ".." || parts[1] == "." || parts[1] == ".." {
		return "", errors.New("GitHub 仓库地址无效")
	}
	if len(parts) > 2 && parts[2] != "releases" {
		return "", errors.New("请输入 GitHub 仓库或 Release 地址")
	}
	return parts[0] + "/" + strings.TrimSuffix(parts[1], ".git"), nil
}

type ClientReleaseAsset struct {
	ID                 int64  `json:"id"`
	Name               string `json:"name"`
	Platform           string `json:"platform"`
	Arch               string `json:"arch"`
	Size               int64  `json:"size"`
	SHA256             string `json:"sha256"`
	BrowserDownloadURL string `json:"-"`
}
type ClientGitHubRelease struct {
	TagName   string               `json:"tagName"`
	HTMLURL   string               `json:"htmlUrl"`
	FetchedAt int64                `json:"fetchedAt"`
	Assets    []ClientReleaseAsset `json:"assets"`
	Error     string               `json:"error,omitempty"`
}
type clientGitHubCacheEntry struct {
	mu        sync.Mutex
	nextCheck time.Time
	release   *ClientGitHubRelease
	err       error
}

var clientGitHubReleaseCache sync.Map

func newClientGitHubHTTPClient(stream bool) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ResponseHeaderTimeout = 20 * time.Second
	if enabled, err := new(SettingService).getBool("localProxyEnable"); err == nil && enabled {
		proxy, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", xray.LocalHTTPPort))
		transport.Proxy = http.ProxyURL(proxy)
	}
	timeout := 10 * time.Second
	if stream {
		timeout = 30 * time.Minute
	}
	return &http.Client{Transport: transport, Timeout: timeout, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("GitHub 下载重定向过多")
		}
		if req.URL.Scheme != "https" {
			return errors.New("GitHub 下载仅允许 HTTPS 重定向")
		}
		if req.URL.Hostname() != "api.github.com" {
			req.Header.Del("Authorization")
		}
		return nil
	}}
}
func clientGitHubRequest(ctx context.Context, client *http.Client, endpoint, token, accept string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", "XUI2-client-downloads")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return client.Do(req)
}
func fetchClientGitHubRelease(ctx context.Context, client *http.Client, apiBase, repo, token string) (*ClientGitHubRelease, error) {
	response, err := clientGitHubRequest(ctx, client, apiBase+"/repos/"+repo+"/releases/latest", token, "application/vnd.github+json")
	if err != nil {
		return nil, errors.New("连接 GitHub 失败，请检查网络或本机代理")
	}
	defer response.Body.Close()
	switch response.StatusCode {
	case 200:
	case 401, 403:
		return nil, errors.New("GitHub 令牌无效、缺少仓库 Contents 读取权限或请求已限流")
	case 404:
		return nil, errors.New("未找到正式版 Release，请检查地址以及私有仓库令牌权限")
	default:
		return nil, fmt.Errorf("GitHub 获取 Release 失败 (HTTP %d)", response.StatusCode)
	}
	var raw struct {
		TagName    string `json:"tag_name"`
		HTMLURL    string `json:"html_url"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
		Assets     []struct {
			ID                 int64  `json:"id"`
			Name               string `json:"name"`
			Size               int64  `json:"size"`
			Digest             string `json:"digest"`
			State              string `json:"state"`
			BrowserDownloadURL string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 4*1024*1024)).Decode(&raw); err != nil {
		return nil, errors.New("GitHub Release 数据格式无效")
	}
	if raw.Draft || raw.Prerelease || raw.TagName == "" {
		return nil, errors.New("尚无可用正式版 Release")
	}
	result := &ClientGitHubRelease{TagName: raw.TagName, HTMLURL: raw.HTMLURL, FetchedAt: time.Now().Unix(), Assets: []ClientReleaseAsset{}}
	for _, asset := range raw.Assets {
		platform, arch := clientReleasePlatform(asset.Name)
		if platform == "" || asset.ID <= 0 || asset.Size <= 0 || (asset.State != "" && asset.State != "uploaded") {
			continue
		}
		digest := ""
		if strings.HasPrefix(asset.Digest, "sha256:") {
			digest = strings.TrimPrefix(asset.Digest, "sha256:")
		}
		if asset.BrowserDownloadURL == "" {
			asset.BrowserDownloadURL = "https://github.com/" + repo + "/releases/download/" + url.PathEscape(raw.TagName) + "/" + url.PathEscape(asset.Name)
		}
		result.Assets = append(result.Assets, ClientReleaseAsset{ID: asset.ID, Name: asset.Name, Platform: platform, Arch: arch, Size: asset.Size, SHA256: digest, BrowserDownloadURL: asset.BrowserDownloadURL})
	}
	if len(result.Assets) == 0 {
		return nil, errors.New("最新 Release 没有支持的客户端安装包")
	}
	return result, nil
}
func clientReleasePlatform(filename string) (string, string) {
	name := strings.ToLower(filename)
	ext := strings.ToLower(path.Ext(name))
	platform := ""
	switch ext {
	case ".exe", ".msi":
		platform = "windows"
	case ".dmg", ".pkg":
		platform = "macos"
	case ".apk":
		platform = "android"
	case ".ipa":
		platform = "ios"
	case ".zip":
		if strings.Contains(name, "windows") || strings.Contains(name, "win32") || strings.Contains(name, "win64") {
			platform = "windows"
		} else if strings.Contains(name, "macos") || strings.Contains(name, "darwin") {
			platform = "macos"
		}
	}
	if platform == "" {
		return "", ""
	}
	arch := ""
	switch {
	case strings.Contains(name, "arm64-v8a"):
		arch = "arm64-v8a"
	case strings.Contains(name, "armeabi-v7a"):
		arch = "armeabi-v7a"
	case strings.Contains(name, "aarch64") || strings.Contains(name, "arm64"):
		arch = "arm64"
	case strings.Contains(name, "x86_64") || strings.Contains(name, "amd64") || strings.Contains(name, "x64"):
		arch = "x64"
	case strings.Contains(name, "universal"):
		arch = "universal"
	case strings.Contains(name, "i686") || strings.Contains(name, "x86") || strings.Contains(name, "ia32"):
		arch = "x86"
	}
	if ext == ".zip" && platform == "windows" {
		if arch != "" {
			arch += " · "
		}
		arch += "便携版"
	}
	return platform, arch
}
func (s *ClientService) GitHubRelease(ctx context.Context, force bool) (*ClientGitHubRelease, error) {
	settings, err := s.Settings()
	if err != nil {
		return nil, err
	}
	if !settings.GitHubReleaseEnable {
		return &ClientGitHubRelease{Assets: []ClientReleaseAsset{}}, nil
	}
	repo, err := parseClientGitHubRepo(settings.GitHubReleaseURL)
	if err != nil {
		return nil, err
	}
	credentialKey, err := s.GitHubCredentialKey(settings)
	if err != nil {
		return nil, err
	}
	key := strings.ToLower(repo) + ":" + credentialKey
	value, _ := clientGitHubReleaseCache.LoadOrStore(key, &clientGitHubCacheEntry{})
	entry := value.(*clientGitHubCacheEntry)
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if force || time.Now().After(entry.nextCheck) {
		token, err := s.GitHubAccessToken(ctx, settings)
		var result *ClientGitHubRelease
		if err == nil {
			result, err = fetchClientGitHubRelease(ctx, newClientGitHubHTTPClient(false), "https://api.github.com", repo, token)
		}
		entry.err = err
		entry.nextCheck = time.Now().Add(time.Minute)
		if err == nil {
			entry.release = result
			entry.nextCheck = time.Now().Add(clientGitHubCacheTTL)
		}
	}
	if entry.release == nil {
		return nil, entry.err
	}
	result := *entry.release
	result.Assets = append([]ClientReleaseAsset{}, entry.release.Assets...)
	if entry.err != nil {
		result.Error = entry.err.Error()
	}
	return &result, nil
}
func (s *ClientService) OpenGitHubAsset(ctx context.Context, id int64, downloadRange ...string) (*http.Response, *ClientReleaseAsset, error) {
	settings, err := s.Settings()
	if err != nil {
		return nil, nil, err
	}
	release, err := s.GitHubRelease(ctx, false)
	if err != nil {
		return nil, nil, err
	}
	var selected *ClientReleaseAsset
	for _, asset := range release.Assets {
		if asset.ID == id {
			copy := asset
			selected = &copy
			break
		}
	}
	if selected == nil {
		return nil, nil, errors.New("安装包已更新或不存在，请刷新下载列表")
	}
	repo, err := parseClientGitHubRepo(settings.GitHubReleaseURL)
	if err != nil {
		return nil, nil, err
	}
	token, err := s.GitHubAccessToken(ctx, settings)
	if err != nil {
		return nil, nil, err
	}
	response, err := openClientGitHubAsset(ctx, newClientGitHubHTTPClient(true), "https://api.github.com", repo, token, id, downloadRange...)
	if err != nil {
		return nil, nil, err
	}
	return response, selected, nil
}
func openClientGitHubAsset(ctx context.Context, client *http.Client, apiBase, repo, token string, id int64, downloadRange ...string) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/repos/%s/releases/assets/%d", apiBase, repo, id), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/octet-stream")
	request.Header.Set("User-Agent", "XUI2-client-downloads")
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if len(downloadRange) > 0 {
		request.Header.Set("Range", downloadRange[0])
	}
	if len(downloadRange) > 1 {
		request.Header.Set("If-Range", downloadRange[1])
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, errors.New("GitHub 安装包连接失败，请稍后重试")
	}
	if response.StatusCode != 200 && response.StatusCode != 206 && response.StatusCode != 416 {
		response.Body.Close()
		return nil, fmt.Errorf("GitHub 安装包下载失败 (HTTP %d)", response.StatusCode)
	}
	if ct := response.Header.Get("Content-Type"); strings.Contains(ct, "json") && response.StatusCode != 416 {
		response.Body.Close()
		return nil, errors.New("GitHub 未返回安装包，请检查令牌权限")
	}
	return response, nil
}
