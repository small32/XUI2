package service

import (
	"errors"
	"xui/database"
	"xui/database/model"

	"gorm.io/gorm"
)

const agentReloadKey = "agentReloadPending"

var restartManagedXray = func() error { return new(XrayService).RestartXray(true) }

// ApplyManagedXray records reload intent before restarting. If the restart
// fails after a database change, a later API request retries the same reload.
func ApplyManagedXray() error {
	if err := setValue(database.GetDB(), agentReloadKey, "1"); err != nil {
		return err
	}
	return EnsureManagedXray()
}

func EnsureManagedXray() error {
	var row model.Setting
	err := database.GetDB().Where("key = ?", agentReloadKey).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if err := restartManagedXray(); err != nil {
		return err
	}
	return database.GetDB().Where("key = ?", agentReloadKey).Delete(&model.Setting{}).Error
}
