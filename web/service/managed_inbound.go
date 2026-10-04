package service

import (
	"errors"
	"strconv"
	"xui/database"
	"xui/database/model"

	"gorm.io/gorm"
)

// UpsertManagedInbound gives manager configuration priority at the target port.
// Existing counters survive adoption of local or differently managed inbounds.
func UpsertManagedInbound(in model.Inbound) error {
	return database.GetDB().Transaction(func(tx *gorm.DB) error {
		var existing, occupied model.Inbound
		err := tx.Where("manager_account_id = ?", in.ManagerAccountID).First(&existing).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		portErr := tx.Where("port = ?", in.Port).First(&occupied).Error
		if portErr != nil && !errors.Is(portErr, gorm.ErrRecordNotFound) {
			return portErr
		}
		if errors.Is(err, gorm.ErrRecordNotFound) && portErr == nil {
			existing = occupied
		}
		if existing.Id == 0 {
			var owner model.User
			if err := tx.Where("username = ?", model.AdminUsername).First(&owner).Error; err != nil {
				return err
			}
			in.Id, in.UserId = 0, owner.Id
			in.Up, in.Down = 0, 0
			in.Tag = "inbound-" + strconv.Itoa(in.Port)
			return tx.Create(&in).Error
		}
		if portErr == nil && occupied.Id != existing.Id {
			if err := tx.Delete(&occupied).Error; err != nil {
				return err
			}
			if err := tx.Model(&existing).Updates(map[string]interface{}{"up": gorm.Expr("up + ?", occupied.Up), "down": gorm.Expr("down + ?", occupied.Down)}).Error; err != nil {
				return err
			}
		}
		in.Tag = "inbound-" + strconv.Itoa(in.Port)
		return tx.Model(&model.Inbound{}).Where("id = ?", existing.Id).Select("manager_account_id", "manager_revision", "total", "remark", "enable", "expiry_time", "monthly_reset", "disabled_by", "listen", "port", "protocol", "settings", "stream_settings", "tag", "sniffing").Updates(&in).Error
	})
}
