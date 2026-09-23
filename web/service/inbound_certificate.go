package service

import (
	"encoding/json"
	"fmt"
	"x-ui/database/model"
)

// Only file-based certificates inherit panel paths; inline certificates remain unchanged.
func certificatePaths(raw string) ([]string, error) {
	if raw == "" {
		return nil, nil
	}
	var stream map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &stream); err != nil {
		return nil, err
	}
	var paths []string
	for _, name := range []string{"tlsSettings", "xtlsSettings"} {
		section, _ := stream[name].(map[string]interface{})
		certs, _ := section["certificates"].([]interface{})
		for i, item := range certs {
			cert, _ := item.(map[string]interface{})
			_, a := cert["certificateFile"]
			_, b := cert["keyFile"]
			if a || b {
				paths = append(paths, fmt.Sprintf("$.%s.certificates[%d]", name, i))
			}
		}
	}
	return paths, nil
}

func (s *InboundService) ApplyPanelCertificates(in *model.Inbound) error {
	paths, err := certificatePaths(in.StreamSettings)
	if err != nil || len(paths) == 0 {
		return err
	}
	settings := SettingService{}
	cert, err := settings.GetCertFile()
	if err != nil {
		return err
	}
	key, err := settings.GetKeyFile()
	if err != nil {
		return err
	}
	if cert == "" || key == "" {
		return fmt.Errorf("请先在面板设置中配置证书公钥和密钥文件路径")
	}
	in.StreamSettings, err = replacePanelCertificates(in.StreamSettings, cert, key)
	return err
}

func replacePanelCertificates(raw, cert, key string) (string, error) {
	var stream map[string]interface{}
	if err := json.Unmarshal([]byte(raw), &stream); err != nil {
		return "", err
	}
	for _, name := range []string{"tlsSettings", "xtlsSettings"} {
		section, _ := stream[name].(map[string]interface{})
		certs, _ := section["certificates"].([]interface{})
		for _, item := range certs {
			c, _ := item.(map[string]interface{})
			_, a := c["certificateFile"]
			_, b := c["keyFile"]
			if a || b {
				c["certificateFile"] = cert
				c["keyFile"] = key
			}
		}
	}
	b, err := json.Marshal(stream)
	return string(b), err
}
