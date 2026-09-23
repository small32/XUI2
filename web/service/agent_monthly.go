package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"
	"xui/database"
	"xui/database/model"
	"xui/util/common"

	"gorm.io/gorm"
)

const agentMonthKey = "agentConfirmedMonth"

var agentMonthlyMu sync.Mutex

func nextMonth(month int) int {
	year, m := month/100, month%100
	if m == 12 {
		return (year+1)*100 + 1
	}
	return year*100 + m + 1
}

func previousMonth(t time.Time) int {
	first := time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, t.Location())
	return TrafficYyyymm(first.AddDate(0, 0, -1))
}

// ResetAgentTraffic returns the exact pre-reset counters. A monthly snapshot
// survives retries and manager outages; an old missing period fails closed.
func ResetAgentTraffic(accountID, port, period int, operation string) (json.RawMessage, error) {
	agentMonthlyMu.Lock()
	defer agentMonthlyMu.Unlock()
	var result json.RawMessage
	err := database.GetDB().Transaction(func(tx *gorm.DB) error {
		var prior model.AgentOperation
		if err := tx.Where("operation_id = ?", operation).First(&prior).Error; err == nil {
			result = json.RawMessage(prior.Result)
			return nil
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		var in model.Inbound
		if err := tx.Where("manager_account_id = ? AND port = ?", accountID, port).First(&in).Error; err != nil {
			return err
		}
		var up, down int64
		if period > 0 {
			if !in.MonthlyReset {
				return errors.New("账号不是按月计费")
			}
			var snapshot model.AgentMonthlySnapshot
			err := tx.Where("account_id = ? AND yyyymm = ?", accountID, period).First(&snapshot).Error
			if err == nil {
				up, down = snapshot.Up, snapshot.Down
			} else if errors.Is(err, gorm.ErrRecordNotFound) {
				if period < previousMonth(time.Now().In(common.ShanghaiLocation)) {
					return fmt.Errorf("月份 %d 缺少原始快照，不能准确恢复历史流量", period)
				}
				up, down = in.Up, in.Down
				snapshot = model.AgentMonthlySnapshot{AccountID: accountID, Yyyymm: period, Port: port, Up: up, Down: down, ResetAt: time.Now().Unix()}
				if err := tx.Create(&snapshot).Error; err != nil {
					return err
				}
				if err := tx.Model(&in).UpdateColumns(map[string]interface{}{"up": 0, "down": 0}).Error; err != nil {
					return err
				}
			} else {
				return err
			}
		} else {
			up, down = in.Up, in.Down
			if err := tx.Model(&in).UpdateColumns(map[string]interface{}{"up": 0, "down": 0}).Error; err != nil {
				return err
			}
		}
		b, _ := json.Marshal(map[string]interface{}{"up": up, "down": down, "port": port})
		result = b
		return tx.Create(&model.AgentOperation{OperationID: operation, Kind: "traffic-reset", Result: string(b)}).Error
	})
	return result, err
}

// Agents roll over counters locally even if the manager is temporarily down.
// The manager later fetches the same durable snapshots via the reset API.
func MaybeAgentMonthlyReset() error {
	now := time.Now().In(common.ShanghaiLocation)
	current := TrafficYyyymm(now)
	var row model.Setting
	err := database.GetDB().Where("key = ?", agentMonthKey).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return setValue(database.GetDB(), agentMonthKey, strconv.Itoa(current))
	}
	if err != nil {
		return err
	}
	confirmed, err := strconv.Atoi(row.Value)
	if err != nil {
		return err
	}
	for confirmed < current {
		period := confirmed
		var accounts []model.Inbound
		if err := database.GetDB().Where("manager_account_id > 0 AND monthly_reset = ?", true).Find(&accounts).Error; err != nil {
			return err
		}
		for _, in := range accounts {
			operation := fmt.Sprintf("agent-rollover-%d-%d", period, in.ManagerAccountID)
			if _, err := ResetAgentTraffic(in.ManagerAccountID, in.Port, period, operation); err != nil {
				return err
			}
		}
		confirmed = nextMonth(confirmed)
		if err := setValue(database.GetDB(), agentMonthKey, strconv.Itoa(confirmed)); err != nil {
			return err
		}
	}
	return nil
}
