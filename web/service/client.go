package service

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/url"
	"strings"
	"time"
	"xui/database"
	"xui/database/model"
)

const clientSettingsKey = "clientSettings"

type ClientSettings struct {
	Enabled   bool   `json:"enabled"`
	PublicURL string `json:"publicUrl"`
}
type ClientService struct{}
type ClientTokens struct {
	AccessToken      string `json:"accessToken"`
	RefreshToken     string `json:"refreshToken"`
	TokenType        string `json:"tokenType"`
	ExpiresAt        int64  `json:"expiresAt"`
	RefreshExpiresAt int64  `json:"refreshExpiresAt"`
}

func clientHash(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }
func clientRandom() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
func (s *ClientService) Settings() (*ClientSettings, error) {
	v := &ClientSettings{Enabled: true}
	row, err := getClientSetting()
	if err != nil {
		return nil, err
	}
	if row != "" {
		if err := decodeClientSettings(row, v); err != nil {
			return nil, err
		}
	}
	return v, nil
}
func ValidateClientURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("服务器地址须为完整 HTTP/HTTPS 地址，不含密码、查询参数或片段")
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/"
	return u.String(), nil
}
func (s *ClientService) SaveSettings(v *ClientSettings) error {
	addr, err := ValidateClientURL(v.PublicURL)
	if err != nil {
		return err
	}
	v.PublicURL = addr
	return saveClientSetting(v)
}
func (s *ClientService) Login(port, password string) (*ClientTokens, error) {
	settings, err := s.Settings()
	if err != nil {
		return nil, err
	}
	if !settings.Enabled {
		return nil, errors.New("客户端登录已关闭")
	}
	in := new(InboundService).CheckInboundCredential(port, password)
	if in == nil {
		return nil, errors.New("端口号或密码错误")
	}
	row := model.ClientSession{AccountID: in.Id, CredentialHash: clientHash(password)}
	tokens, err := rotateClientTokens(&row)
	if err != nil {
		return nil, err
	}
	db := database.GetDB()
	if err := db.Where("refresh_expires <= ?", time.Now().Unix()).Delete(&model.ClientSession{}).Error; err != nil {
		return nil, err
	}
	if err := db.Create(&row).Error; err != nil {
		return nil, err
	}
	return tokens, nil
}
func rotateClientTokens(row *model.ClientSession) (*ClientTokens, error) {
	access, err := clientRandom()
	if err != nil {
		return nil, err
	}
	refresh, err := clientRandom()
	if err != nil {
		return nil, err
	}
	row.AccessHash = clientHash(access)
	row.RefreshHash = clientHash(refresh)
	row.AccessExpires = time.Now().Add(24 * time.Hour).Unix()
	row.RefreshExpires = time.Now().Add(30 * 24 * time.Hour).Unix()
	return &ClientTokens{AccessToken: access, RefreshToken: refresh, TokenType: "Bearer", ExpiresAt: row.AccessExpires, RefreshExpiresAt: row.RefreshExpires}, nil
}
func (s *ClientService) Authenticate(token string, refresh bool) (*model.ClientSession, *model.Inbound, error) {
	settings, err := s.Settings()
	if err != nil || !settings.Enabled {
		return nil, nil, errors.New("客户端登录已关闭")
	}
	if len(token) != 64 {
		return nil, nil, errors.New("登录已失效，请重新登录")
	}
	column := "access_hash"
	if refresh {
		column = "refresh_hash"
	}
	var row model.ClientSession
	if err := database.GetDB().Where(column+" = ?", clientHash(token)).First(&row).Error; err != nil {
		return nil, nil, errors.New("登录已失效，请重新登录")
	}
	expiry := row.AccessExpires
	if refresh {
		expiry = row.RefreshExpires
	}
	in, err := new(InboundService).GetInbound(row.AccountID)
	if expiry <= time.Now().Unix() || err != nil || in == nil || row.CredentialHash != clientHash(inboundPassword(in)) {
		return nil, nil, errors.New("登录已失效，请重新登录")
	}
	return &row, in, nil
}
func (s *ClientService) Refresh(token string) (*ClientTokens, error) {
	row, _, err := s.Authenticate(token, true)
	if err != nil {
		return nil, err
	}
	old := row.RefreshHash
	tokens, err := rotateClientTokens(row)
	if err != nil {
		return nil, err
	}
	result := database.GetDB().Model(&model.ClientSession{}).Where("id = ? AND refresh_hash = ?", row.Id, old).Updates(map[string]interface{}{"access_hash": row.AccessHash, "refresh_hash": row.RefreshHash, "access_expires": row.AccessExpires, "refresh_expires": row.RefreshExpires})
	if result.Error != nil {
		return nil, result.Error
	}
	if result.RowsAffected != 1 {
		return nil, errors.New("刷新令牌已使用，请重新登录")
	}
	return tokens, nil
}

type ClientAccount struct {
	ConfigError     string `json:"configError,omitempty"`
	Port            int    `json:"port"`
	Remark          string `json:"remark"`
	UsedBytes       int64  `json:"usedBytes"`
	ActualUsedBytes int64  `json:"actualUsedBytes"`
	TotalBytes      int64  `json:"totalBytes"`
	RemainingBytes  *int64 `json:"remainingBytes"`
	Unlimited       bool   `json:"unlimited"`
	ExpiresAt       *int64 `json:"expiresAt"`
	Status          string `json:"status"`
	CanProxy        bool   `json:"canProxy"`
	MonthlyReset    bool   `json:"monthlyReset"`
	ObservedAt      *int64 `json:"observedAt"`
	Stale           bool   `json:"stale"`
	ConfigVersion   string `json:"configVersion"`
	RenewalDays     []int  `json:"renewalDays"`
}

func (s *ClientService) Account(in *model.Inbound, baseURL string) (*ClientAccount, error) {
	manager := new(ServerManagementService)
	rows, err := manager.Summary(in.Id)
	if err != nil {
		return nil, err
	}
	if len(rows) != 1 {
		return nil, errors.New("账号不存在")
	}
	usage, err := manager.NodeUsage(in.Port)
	if err != nil {
		return nil, err
	}
	settings, err := manager.GetSetting()
	if err != nil {
		return nil, err
	}
	v := &ClientAccount{Port: in.Port, Remark: in.Remark, UsedBytes: rows[0].Total, ActualUsedBytes: in.Up + in.Down, TotalBytes: in.Total, Unlimited: in.Total <= 0, MonthlyReset: in.MonthlyReset, Status: "active", CanProxy: true, RenewalDays: []int{14, 7, 3}}
	if !v.Unlimited {
		remaining := in.Total - v.UsedBytes
		if remaining < 0 {
			remaining = 0
		}
		v.RemainingBytes = &remaining
	}
	if in.ExpiryTime > 0 {
		expiry := in.ExpiryTime / 1000
		v.ExpiresAt = &expiry
	}
	if !in.Enable {
		v.Status = "disabled"
		v.CanProxy = false
	}
	if TrafficOverlimit(0, v.UsedBytes, in.Total) {
		v.Status = "overlimit"
		v.CanProxy = false
	}
	if in.ExpiryTime > 0 && in.ExpiryTime <= time.Now().UnixMilli() {
		v.Status = "expired"
		v.CanProxy = false
	}
	if len(usage) == 0 {
		v.Stale = true
		v.CanProxy = false
		if v.Status == "active" {
			v.Status = "no_nodes"
		}
	}
	var raw []model.NodeTraffic
	if err := database.GetDB().Where("account_id = ? OR (account_id < 0 AND port = ?)", in.Id, in.Port).Find(&raw).Error; err != nil {
		return nil, err
	}
	for _, reading := range raw {
		v.ActualUsedBytes += reading.Up + reading.Down
	}
	for _, node := range usage {
		if node.ObservedAt <= 0 || node.LastError != "" || time.Since(time.Unix(node.ObservedAt, 0)) > time.Duration(settings.HeartbeatMinutes*2)*time.Minute {
			v.Stale = true
		}
		if node.ObservedAt > 0 && (v.ObservedAt == nil || node.ObservedAt < *v.ObservedAt) {
			observed := node.ObservedAt
			v.ObservedAt = &observed
		}
	}
	yaml, err := s.ClashConfig(in, baseURL)
	if err == nil {
		v.ConfigVersion = clientHash(string(yaml))
	} else {
		v.CanProxy = false
		v.ConfigError = err.Error()
		if v.Status == "active" {
			v.Status = "invalid_config"
		}
	}
	return v, nil
}

func ClientConfigHash(data []byte) string { return clientHash(string(data)) }
