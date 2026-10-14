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
func decodeClientSettings(raw string, v *ClientSettings) error { return json.Unmarshal([]byte(raw), v) }
func saveClientSetting(v *ClientSettings) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return setValue(database.GetDB(), clientSettingsKey, string(b))
}
