package service

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"xui/database"
	"xui/database/model"
	"xui/util/common"
	"xui/xray"

	"gorm.io/gorm"
)

type InboundService struct {
}

func (s *InboundService) GetInbounds(userId int) ([]*model.Inbound, error) {
	db := database.GetDB()
	var inbounds []*model.Inbound
	err := db.Model(model.Inbound{}).Where("user_id = ?", userId).Find(&inbounds).Error
	if err != nil && err != gorm.ErrRecordNotFound {
		return nil, err
	}
	return inbounds, nil
}

func (s *InboundService) GetAllInbounds() ([]*model.Inbound, error) {
	db := database.GetDB()
	var inbounds []*model.Inbound
	err := db.Model(model.Inbound{}).Find(&inbounds).Error
	if err != nil && err != gorm.ErrRecordNotFound {
		return nil, err
	}
	return inbounds, nil
}

func (s *InboundService) checkPortExist(port int, ignoreId int) (bool, error) {
	db := database.GetDB()
	db = db.Model(model.Inbound{}).Where("port = ?", port)
	if ignoreId > 0 {
		db = db.Where("id != ?", ignoreId)
	}
	var count int64
	err := db.Count(&count).Error
	if err != nil {
		return false, err
	}
	return count > 0, nil
}

// CheckInboundRemark 校验入站用户名（remark）是否与面板管理员账号重名。
// 登录流程先校验管理员、失败后再按 remark 匹配入站完成受限登录，
// 重名会让受限账号顶着管理员用户名登录，因此禁止使用。
func (s *InboundService) CheckInboundRemark(remark string) error {
	remark = strings.TrimSpace(remark)
	if remark == "" {
		return nil
	}
	var count int64
	if err := database.GetDB().Model(model.User{}).Where("username = ?", remark).Count(&count).Error; err != nil {
		return err
	}
	if count > 0 {
		return common.NewErrorf("非法的用户名")
	}
	return nil
}

func (s *InboundService) AddInbound(inbound *model.Inbound) error {
	exist, err := s.checkPortExist(inbound.Port, 0)
	if err != nil {
		return err
	}
	if exist {
		return common.NewError("端口已存在:", inbound.Port)
	}
	db := database.GetDB()
	return db.Save(inbound).Error
}

func (s *InboundService) AddInbounds(inbounds []*model.Inbound) error {
	for _, inbound := range inbounds {
		exist, err := s.checkPortExist(inbound.Port, 0)
		if err != nil {
			return err
		}
		if exist {
			return common.NewError("端口已存在:", inbound.Port)
		}
	}

	db := database.GetDB()
	tx := db.Begin()
	var err error
	defer func() {
		if err == nil {
			tx.Commit()
		} else {
			tx.Rollback()
		}
	}()

	for _, inbound := range inbounds {
		err = tx.Save(inbound).Error
		if err != nil {
			return err
		}
	}

	return nil
}

func (s *InboundService) DelInbound(id int) error {
	db := database.GetDB()
	return db.Delete(model.Inbound{}, id).Error
}

func (s *InboundService) GetInbound(id int) (*model.Inbound, error) {
	db := database.GetDB()
	inbound := &model.Inbound{}
	err := db.Model(model.Inbound{}).First(inbound, id).Error
	if err != nil {
		return nil, err
	}
	return inbound, nil
}

// GetInboundByPort 按端口号取入站
func (s *InboundService) GetInboundByPort(port int) (*model.Inbound, error) {
	db := database.GetDB()
	inbound := &model.Inbound{}
	err := db.Model(model.Inbound{}).Where("port = ?", port).First(inbound).Error
	if err != nil {
		return nil, err
	}
	return inbound, nil
}

// CheckInboundCredential 校验受限登录凭据：账号=入站"备注"（即入站用户名），密码=入站密码。
// 仅支持 trojan / shadowsocks / socks / http 四种带密码的协议，其余返回 nil。
func (s *InboundService) CheckInboundCredential(username, password string) *model.Inbound {
	if username == "" || password == "" {
		return nil
	}
	var inbound model.Inbound
	if err := database.GetDB().Where("remark = ?", username).First(&inbound).Error; err != nil || inbound.Id == 0 {
		return nil
	}
	expected := inboundPassword(&inbound)
	if expected == "" || expected != password {
		return nil
	}
	return &inbound
}

// GetInboundPassword 按入站 id 取其当前密码，用于受限登录会话内脱敏回显。
// 受限请求不依赖客户端会话中的密码快照（方案避免把入站密码写进 Cookie）。
func (s *InboundService) GetInboundPassword(id int) (string, bool) {
	inbound, err := s.GetInbound(id)
	if err != nil || inbound == nil {
		return "", false
	}
	password := inboundPassword(inbound)
	if password == "" {
		return "", false
	}
	return password, true
}

// inboundPassword 按协议从入站 settings 中取"密码"，口径与面板详细信息弹窗一致
func inboundPassword(inbound *model.Inbound) string {
	var settings map[string]interface{}
	if err := json.Unmarshal([]byte(inbound.Settings), &settings); err != nil {
		return ""
	}
	switch inbound.Protocol {
	case model.Trojan:
		// settings.clients[0].password
		return arrayFieldPassword(settings["clients"], "password")
	case model.Shadowsocks:
		if v, ok := settings["password"].(string); ok {
			return v
		}
	case model.Socks, model.Http:
		// settings.accounts[0].pass
		return arrayFieldPassword(settings["accounts"], "pass")
	}
	return ""
}

// arrayFieldPassword 取数组首个元素的指定字符串字段
func arrayFieldPassword(raw interface{}, field string) string {
	arr, ok := raw.([]interface{})
	if !ok || len(arr) == 0 {
		return ""
	}
	obj, ok := arr[0].(map[string]interface{})
	if !ok {
		return ""
	}
	if v, ok := obj[field].(string); ok {
		return v
	}
	return ""
}

func (s *InboundService) UpdateInbound(inbound *model.Inbound) error {
	exist, err := s.checkPortExist(inbound.Port, inbound.Id)
	if err != nil {
		return err
	}
	if exist {
		return common.NewError("端口已存在:", inbound.Port)
	}

	db := database.GetDB()
	return db.Transaction(func(tx *gorm.DB) error {
		oldInbound := &model.Inbound{}
		if err := tx.Where("id = ?", inbound.Id).First(oldInbound).Error; err != nil {
			return err
		}
		disabledBy := oldInbound.DisabledBy
		if inbound.Enable {
			disabledBy = ""
		} else if inbound.DisabledBy != "" {
			disabledBy = inbound.DisabledBy
		} else if oldInbound.Enable && disabledBy == "" {
			disabledBy = "manual"
		}
		// Update only editable columns. AddTraffic increments up/down in SQL, so
		// an edit racing with a traffic sample cannot write stale counters back.
		updates := map[string]interface{}{
			"total": inbound.Total, "remark": inbound.Remark, "enable": inbound.Enable,
			"disabled_by": disabledBy, "expiry_time": inbound.ExpiryTime,
			"monthly_reset": inbound.MonthlyReset, "listen": inbound.Listen,
			"port": inbound.Port, "protocol": inbound.Protocol, "settings": inbound.Settings,
			"stream_settings": inbound.StreamSettings, "sniffing": inbound.Sniffing,
			"tag":              fmt.Sprintf("inbound-%v", inbound.Port),
			"manager_revision": inbound.ManagerRevision,
		}
		result := tx.Model(&model.Inbound{}).Where("id = ?", inbound.Id).Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("入站不存在或已被删除")
		}
		return nil
	})
}

// ResetTraffic 将指定入站的 up/down 清零，用于"重置流量"。
// 独立于 UpdateInbound：编辑入站时不应覆盖已累加的流量，
// 而重置是一个显式清零动作，走独立的写库路径。
func (s *InboundService) ResetTraffic(id int) error {
	return database.GetDB().Model(&model.Inbound{}).
		Where("id = ?", id).
		UpdateColumns(map[string]interface{}{"up": 0, "down": 0}).
		Error
}

func (s *InboundService) AddTraffic(traffics []*xray.Traffic) (err error) {
	if len(traffics) == 0 {
		return nil
	}
	db := database.GetDB()
	db = db.Model(model.Inbound{})
	tx := db.Begin()
	if tx.Error != nil {
		return tx.Error
	}
	defer func() {
		if err != nil {
			tx.Rollback()
		} else {
			// The caller advances Xray's sampling baseline only when this
			// function returns nil, so a failed commit must be reported too.
			err = tx.Commit().Error
		}
	}()
	for _, traffic := range traffics {
		if traffic.IsInbound {
			err = tx.Where("tag = ?", traffic.Tag).
				UpdateColumn("up", gorm.Expr("up + ?", traffic.Up)).
				UpdateColumn("down", gorm.Expr("down + ?", traffic.Down)).
				Error
			if err != nil {
				return
			}
		}
	}
	return
}

// expiredAt 判断入站是否已过到期时间（毫秒时间戳，0 表示无限期）。
func expiredAt(in *model.Inbound, nowMs int64) bool {
	return in.ExpiryTime > 0 && in.ExpiryTime <= nowMs
}

// DisableInvalidInbounds only enforces expiration on an agent. Shared traffic
// limits belong to the manager, which has usage from all agents.
func (s *InboundService) DisableInvalidInbounds() (int64, error) {
	db := database.GetDB()
	var enabled []model.Inbound
	if err := db.Where("enable = ?", true).Find(&enabled).Error; err != nil {
		return 0, err
	}
	ids := make([]int, 0)
	now := time.Now().UnixMilli()
	for i := range enabled {
		if expiredAt(&enabled[i], now) {
			ids = append(ids, enabled[i].Id)
		}
	}
	if len(ids) == 0 {
		return 0, nil
	}
	err := db.Model(&model.Inbound{}).Where("id IN ?", ids).Updates(map[string]interface{}{"enable": false, "disabled_by": "expired"}).Error
	return int64(len(ids)), err
}
