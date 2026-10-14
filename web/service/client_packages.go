package service

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"xui/config"
	"xui/database"
	"xui/database/model"
)

const ClientPackageMaxSize int64 = 1024 * 1024 * 1024

var clientDownloadMu sync.Mutex
var clientDownloads = map[string]bool{}

func AcquireClientDownload(account string) (func(), bool) {
	clientDownloadMu.Lock()
	defer clientDownloadMu.Unlock()
	if clientDownloads[account] {
		return nil, false
	}
	clientDownloads[account] = true
	return func() { clientDownloadMu.Lock(); delete(clientDownloads, account); clientDownloadMu.Unlock() }, true
}
func ClientPackageDir() string {
	return filepath.Join(filepath.Dir(config.GetDBPath()), "client-packages")
}
func ValidateClientPackage(p *model.ClientPackage) error {
	switch p.Platform {
	case "windows", "macos", "android", "ios":
	default:
		return errors.New("平台须为 windows、macos、android 或 ios")
	}
	if strings.TrimSpace(p.Version) == "" {
		return errors.New("请输入客户端版本")
	}
	if p.ExternalURL != "" {
		u, err := url.Parse(p.ExternalURL)
		if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil {
			return errors.New("分发链接须为不含登录密码的 HTTP/HTTPS 地址")
		}
	}
	return nil
}
func (s *ClientService) Packages(publishedOnly bool) ([]model.ClientPackage, error) {
	rows := []model.ClientPackage{}
	q := database.GetDB()
	if publishedOnly {
		q = q.Where("published = ?", true)
	}
	err := q.Order("platform, arch, id desc").Find(&rows).Error
	return rows, err
}
func (s *ClientService) StorePackage(p *model.ClientPackage, r io.Reader, dir string) error {
	if err := ValidateClientPackage(p); err != nil {
		return err
	}
	p.Id = 0
	p.CreatedAt = time.Now().Unix()
	if p.ExternalURL != "" {
		p.Filename = ""
		p.StorageName = ""
		p.Size = 0
		p.SHA256 = ""
		return database.GetDB().Create(p).Error
	}
	ext := strings.ToLower(filepath.Ext(p.Filename))
	allowed := map[string]bool{"windows": ext == ".exe" || ext == ".msi" || ext == ".zip", "macos": ext == ".dmg" || ext == ".pkg" || ext == ".zip", "android": ext == ".apk", "ios": ext == ".ipa"}
	if !allowed[p.Platform] || r == nil {
		return errors.New("安装包格式不匹配平台")
	}
	p.Filename = filepath.Base(strings.ReplaceAll(p.Filename, "\\", "/"))
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	id, err := clientRandom()
	if err != nil {
		return err
	}
	p.StorageName = id + ext
	target := filepath.Join(dir, p.StorageName)
	file, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	h := sha256.New()
	size, copyErr := io.Copy(io.MultiWriter(file, h), io.LimitReader(r, ClientPackageMaxSize+1))
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil || size == 0 || size > ClientPackageMaxSize {
		os.Remove(target)
		return errors.New("安装包写入失败、为空或超过 1GB")
	}
	p.Size = size
	p.SHA256 = hex.EncodeToString(h.Sum(nil))
	if err := database.GetDB().Create(p).Error; err != nil {
		os.Remove(target)
		return err
	}
	return nil
}
func (s *ClientService) DeletePackage(id int, dir string) error {
	var p model.ClientPackage
	if err := database.GetDB().First(&p, id).Error; err != nil {
		return err
	}
	if p.StorageName != "" {
		err := os.Remove(filepath.Join(dir, filepath.Base(p.StorageName)))
		if err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return database.GetDB().Delete(&p).Error
}
