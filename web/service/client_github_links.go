package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func (s *ClientService) GitHubRelayEnabled() (bool, error) {
	cfg, err := s.Settings()
	if err != nil {
		return false, err
	}
	switch cfg.GitHubDownloadMode {
	case "relay":
		return true, nil
	case "direct":
		return false, nil
	}
	return new(SettingService).getBool("localProxyEnable")
}
func (s *ClientService) DirectGitHubAssetURL(ctx context.Context, id int64) (string, error) {
	cfg, err := s.Settings()
	if err != nil {
		return "", err
	}
	release, err := s.GitHubRelease(ctx, false)
	if err != nil {
		return "", err
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
		return "", errors.New("安装包已更新或不存在，请刷新下载列表")
	}
	if cfg.GitHubToken == "" {
		return selected.BrowserDownloadURL, nil
	}
	repo, err := parseClientGitHubRepo(cfg.GitHubReleaseURL)
	if err != nil {
		return "", err
	}
	return resolveClientGitHubAssetURL(ctx, newClientGitHubHTTPClient(false), "https://api.github.com", repo, cfg.GitHubToken, id)
}

// Only inspect the GitHub redirect. No package bytes pass through XUI here.
func resolveClientGitHubAssetURL(ctx context.Context, client *http.Client, apiBase, repo, token string, id int64) (string, error) {
	copyClient := *client
	copyClient.CheckRedirect = func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }
	endpoint := fmt.Sprintf("%s/repos/%s/releases/assets/%d?xui=%s", apiBase, repo, id, strconv.FormatInt(time.Now().UnixNano(), 10))
	response, err := clientGitHubRequest(ctx, &copyClient, endpoint, token, "application/octet-stream")
	if err != nil {
		return "", errors.New("无法获取 GitHub 临时下载链接，请稍后重试或使用中转下载")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusFound && response.StatusCode != http.StatusTemporaryRedirect {
		return "", errors.New("GitHub 未提供临时下载链接，请使用中转下载")
	}
	raw := response.Header.Get("Location")
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
		return "", errors.New("GitHub 下载链接无效")
	}
	if token != "" && strings.Contains(raw, token) {
		return "", errors.New("GitHub 下载链接格式无效")
	}
	return u.String(), nil
}
