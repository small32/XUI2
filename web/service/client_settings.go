package service

import (
	"encoding/json"
	"errors"
	"gorm.io/gorm"
	"xui/database"
	"xui/database/model"
)

func getClientSetting() (string, error) {
	var row model.Setting
	err := database.GetDB().Where("key = ?", clientSettingsKey).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil
	}
	return row.Value, err
}
func decodeClientSettings(raw string, v *ClientSettings) error {
	if err := json.Unmarshal([]byte(raw), v); err != nil {
		return err
	}
	var secret struct {
		GitHubToken string `json:"githubToken"`
	}
	if err := json.Unmarshal([]byte(raw), &secret); err != nil {
		return err
	}
	v.GitHubToken = secret.GitHubToken
	v.GitHubTokenConfigured = v.GitHubToken != ""
	return nil
}
func saveClientSetting(v *ClientSettings) error {
	b, err := json.Marshal(struct {
		*ClientSettings
		GitHubToken string `json:"githubToken"`
	}{v, v.GitHubToken})
	if err != nil {
		return err
	}
	return setValue(database.GetDB(), clientSettingsKey, string(b))
}
