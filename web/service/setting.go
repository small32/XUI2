package service

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"
	"xui/database"
	"xui/database/model"
	"xui/logger"
	"xui/util/common"
	"xui/util/random"
	"xui/util/reflect_util"
	"xui/web/entity"
)

//go:embed config.json
var xrayTemplateConfig string

var activePanelCert struct {
	sync.RWMutex
	fingerprint string
	initialized bool
}

// SetActivePanelCertificate records the certificate loaded by the HTTPS listener.
// Reading the file again would report a new certificate before the panel restarts.
func SetActivePanelCertificate(der []byte) {
	activePanelCert.Lock()
	defer activePanelCert.Unlock()
	activePanelCert.initialized = true
	activePanelCert.fingerprint = ""
	if len(der) > 0 {
		sum := sha256.Sum256(der)
		activePanelCert.fingerprint = hex.EncodeToString(sum[:])
	}
}

var defaultValueMap = map[string]string{
	"serverName":            "主服务器",
	"xrayTemplateConfig":    xrayTemplateConfig,
	"webListen":             "",
	"webPort":               "54321",
	"webCertFile":           "",
	"webKeyFile":            "",
	"secret":                random.Seq(32),
	"webBasePath":           "/",
	"timeLocation":          "Asia/Shanghai",
	"restrictedLoginEnable": "true",
	"externalHost":          "",
}

type SettingService struct {
}

func (s *SettingService) GetAllSetting() (*entity.AllSetting, error) {
	db := database.GetDB()
	settings := make([]*model.Setting, 0)
	err := db.Model(model.Setting{}).Find(&settings).Error
	if err != nil {
		return nil, err
	}
	allSetting := &entity.AllSetting{}
	t := reflect.TypeOf(allSetting).Elem()
	v := reflect.ValueOf(allSetting).Elem()
	fields := reflect_util.GetFields(t)

	setSetting := func(key, value string) (err error) {
		defer func() {
			panicErr := recover()
			if panicErr != nil {
				err = errors.New(fmt.Sprint(panicErr))
			}
		}()

		var found bool
		var field reflect.StructField
		for _, f := range fields {
			if f.Tag.Get("json") == key {
				field = f
				found = true
				break
			}
		}

		if !found {
			// 有些设置自动生成，不需要返回到前端给用户修改
			return nil
		}

		fieldV := v.FieldByName(field.Name)
		switch t := fieldV.Interface().(type) {
		case int:
			n, err := strconv.ParseInt(value, 10, 64)
			if err != nil {
				return err
			}
			fieldV.SetInt(n)
		case string:
			fieldV.SetString(value)
		case bool:
			fieldV.SetBool(value == "true")
		default:
			return common.NewErrorf("unknown field %v type %v", key, t)
		}
		return
	}

	keyMap := map[string]bool{}
	for _, setting := range settings {
		err := setSetting(setting.Key, setting.Value)
		if err != nil {
			return nil, err
		}
		keyMap[setting.Key] = true
	}

	for key, value := range defaultValueMap {
		if keyMap[key] {
			continue
		}
		err := setSetting(key, value)
		if err != nil {
			return nil, err
		}
	}

	// 证书路径未显式配置时，默认指向 /root/cert 下的 fullchain.cer 与匹配的 *.key，
	// 便于面板自动接管一键申请生成的可信证书（可在设置页据此看到实际生效路径）。
	if allSetting.WebCertFile == "" || allSetting.WebKeyFile == "" {
		defCert, defKey := defaultPanelCert()
		if allSetting.WebCertFile == "" {
			allSetting.WebCertFile = defCert
		}
		if allSetting.WebKeyFile == "" {
			allSetting.WebKeyFile = defKey
		}
	}

	return allSetting, nil
}

func (s *SettingService) ResetSettings() error {
	db := database.GetDB()
	return db.Where("1 = 1").Delete(model.Setting{}).Error
}

func (s *SettingService) getSetting(key string) (*model.Setting, error) {
	db := database.GetDB()
	setting := &model.Setting{}
	err := db.Model(model.Setting{}).Where("key = ?", key).First(setting).Error
	if err != nil {
		return nil, err
	}
	return setting, nil
}

func (s *SettingService) saveSetting(key string, value string) error {
	setting, err := s.getSetting(key)
	db := database.GetDB()
	if database.IsNotFound(err) {
		return db.Create(&model.Setting{
			Key:   key,
			Value: value,
		}).Error
	} else if err != nil {
		return err
	}
	setting.Key = key
	setting.Value = value
	return db.Save(setting).Error
}

func (s *SettingService) getString(key string) (string, error) {
	setting, err := s.getSetting(key)
	if database.IsNotFound(err) {
		value, ok := defaultValueMap[key]
		if !ok {
			return "", common.NewErrorf("key <%v> not in defaultValueMap", key)
		}
		return value, nil
	} else if err != nil {
		return "", err
	}
	return setting.Value, nil
}

func (s *SettingService) setString(key string, value string) error {
	return s.saveSetting(key, value)
}

func (s *SettingService) getBool(key string) (bool, error) {
	str, err := s.getString(key)
	if err != nil {
		return false, err
	}
	return strconv.ParseBool(str)
}

func (s *SettingService) setBool(key string, value bool) error {
	return s.setString(key, strconv.FormatBool(value))
}

// IsRestrictedLoginEnabled 是否允许非管理员用户（入站备注/用户名+入站密码）登录。
// 默认允许；关闭后管理员账号校验失败时不再尝试受限登录。
func (s *SettingService) IsRestrictedLoginEnabled() (bool, error) {
	return s.getBool("restrictedLoginEnable")
}

func (s *SettingService) getInt(key string) (int, error) {
	str, err := s.getString(key)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(str)
}

func (s *SettingService) setInt(key string, value int) error {
	return s.setString(key, strconv.Itoa(value))
}

func (s *SettingService) GetXrayConfigTemplate() (string, error) {
	return s.getString("xrayTemplateConfig")
}

func (s *SettingService) GetListen() (string, error) {
	return s.getString("webListen")
}

func (s *SettingService) GetPort() (int, error) {
	return s.getInt("webPort")
}

func (s *SettingService) SetPort(port int) error {
	return s.setInt("webPort", port)
}

func (s *SettingService) GetCertFile() (string, error) {
	c, err := s.getString("webCertFile")
	if err != nil {
		return "", err
	}
	if c == "" {
		defCert, _ := defaultPanelCert()
		return defCert, nil
	}
	return c, nil
}

func (s *SettingService) GetKeyFile() (string, error) {
	k, err := s.getString("webKeyFile")
	if err != nil {
		return "", err
	}
	if k == "" {
		_, defKey := defaultPanelCert()
		return defKey, nil
	}
	return k, nil
}

// defaultPanelCert 返回 /root/cert 目录下的默认证书路径：
// 证书为 fullchain.cer，私钥优先 fullchain.key，否则取目录中第一个 *.key。
// /root/cert 不存在或无匹配文件时返回空串。
func defaultPanelCert() (cert, key string) {
	return panelCertIn("/root/cert")
}

// panelCertIn 在给定目录 A 中确定默认证书路径：证书为 fullchain.cer，
// 私钥优先 fullchain.key，否则取目录中第一个 *.key。目录无匹配或访问失败返回空串。
func panelCertIn(dir string) (cert, key string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", ""
	}
	cert = filepath.Join(dir, "fullchain.cer")
	if _, err := os.Stat(cert); err != nil {
		cert = ""
	}
	key = filepath.Join(dir, "fullchain.key")
	if _, err := os.Stat(key); err != nil {
		key = ""
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			if strings.HasSuffix(e.Name(), ".key") {
				key = filepath.Join(dir, e.Name())
				break
			}
		}
	}
	return cert, key
}

func (s *SettingService) GetExternalHost() (string, error) {
	return s.getString("externalHost")
}

// AgentConnectionInfo 计算被控端对外暴露的连接信息（仅 agent 使用）。
// host 取手工填写的 ExternalHost，否则回退监听地址；token 与指纹动态读取、不落库。
func (s *SettingService) AgentConnectionInfo() (*entity.AgentConnectionInfo, error) {
	host, err := s.GetExternalHost()
	if err != nil {
		return nil, err
	}
	port, err := s.GetPort()
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(host) == "" {
		if listen, e := s.getListenHost(); e == nil {
			host = listen
		}
	}
	host = strings.TrimSpace(host)
	// 展示给管理端填写的 API 地址为站点根（不含 /api/v1，管理端会自行拼接该路径），
	// 订阅地址只保留主机名（不带 Web 端口）。
	api := "https://" + host
	if port != 0 {
		api += ":" + strconv.Itoa(port)
	}
	info := &entity.AgentConnectionInfo{
		ApiUrl:       api,
		SubscribeUrl: "https://" + host,
		Port:         port,
		Token:        os.Getenv("XUI_AGENT_TOKEN"),
		CertSha256:   s.activeCertFingerprint(),
	}
	return info, nil
}

func (s *SettingService) activeCertFingerprint() string {
	activePanelCert.RLock()
	fingerprint := activePanelCert.fingerprint
	initialized := activePanelCert.initialized
	activePanelCert.RUnlock()
	if initialized {
		if fingerprint == "" {
			return "-"
		}
		return fingerprint
	}
	return s.selfCertFingerprint()
}

// getListenHost 返回 webListen 设置；若为空，尝试返回本机非回环 IPv4 地址。
func (s *SettingService) getListenHost() (string, error) {
	listen, err := s.getString("webListen")
	if err != nil || strings.TrimSpace(listen) != "" {
		return listen, err
	}
	addrs, perr := net.InterfaceAddrs()
	if perr != nil {
		return "", perr
	}
	for _, addr := range addrs {
		ipnet, ok := addr.(*net.IPNet)
		if !ok || ipnet.IP.IsLoopback() || ipnet.IP.To4() == nil {
			continue
		}
		return ipnet.IP.String(), nil
	}
	return "", fmt.Errorf("无法确定本机 IP")
}

// selfCertFingerprint 读取当前面板证书并返回其 SHA256 指纹（hex 小写）。
// 证书路径失败或读取失败时返回 "-"，不返回错误，避免初始化被阻断。
func (s *SettingService) selfCertFingerprint() string {
	certFile, err := s.GetCertFile()
	if err != nil || strings.TrimSpace(certFile) == "" {
		certFile = "/root/cert/fullchain.cer"
	}
	data, err := os.ReadFile(certFile)
	if err != nil {
		return "-"
	}
	// 管理端按叶子证书 DER（cert.Raw）固定指纹，这里必须用同一口径；
	// 直接哈希 PEM 文本会得到完全不同的值。
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		return "-"
	}
	sum := sha256.Sum256(block.Bytes)
	return hex.EncodeToString(sum[:])
}

func (s *SettingService) SetCertFiles(cert, key string) error {
	if cert == "" || key == "" {
		return fmt.Errorf("证书和密钥路径不能为空")
	}
	if err := s.setString("webCertFile", cert); err != nil {
		return err
	}
	return s.setString("webKeyFile", key)
}

func (s *SettingService) GetSecret() ([]byte, error) {
	secret, err := s.getString("secret")
	if secret == defaultValueMap["secret"] {
		err := s.saveSetting("secret", secret)
		if err != nil {
			logger.Warning("save secret failed:", err)
		}
	}
	return []byte(secret), err
}

func (s *SettingService) GetBasePath() (string, error) {
	basePath, err := s.getString("webBasePath")
	if err != nil {
		return "", err
	}
	if !strings.HasPrefix(basePath, "/") {
		basePath = "/" + basePath
	}
	if !strings.HasSuffix(basePath, "/") {
		basePath += "/"
	}
	return basePath, nil
}

func (s *SettingService) GetTimeLocation() (*time.Location, error) {
	l, err := s.getString("timeLocation")
	if err != nil {
		return nil, err
	}
	location, err := time.LoadLocation(l)
	if err != nil {
		defaultLocation := defaultValueMap["timeLocation"]
		logger.Errorf("location <%v> not exist, using default location: %v", l, defaultLocation)
		return time.LoadLocation(defaultLocation)
	}
	return location, nil
}

func (s *SettingService) UpdateAllSetting(allSetting *entity.AllSetting) error {
	if err := allSetting.CheckValid(); err != nil {
		return err
	}

	v := reflect.ValueOf(allSetting).Elem()
	t := reflect.TypeOf(allSetting).Elem()
	fields := reflect_util.GetFields(t)
	errs := make([]error, 0)
	for _, field := range fields {
		key := field.Tag.Get("json")
		fieldV := v.FieldByName(field.Name)
		value := fmt.Sprint(fieldV.Interface())
		err := s.saveSetting(key, value)
		if err != nil {
			errs = append(errs, err)
		}
	}
	return common.Combine(errs...)
}
