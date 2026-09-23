package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"
	"xui/database"
	"xui/database/model"
	"xui/util/common"
	"xui/web/entity"

	"gorm.io/gorm"
)

var trafficMu sync.Mutex

type NodeUsage struct {
	NodeID     int    `json:"nodeId"`
	Name       string `json:"name"`
	RemotePort int    `json:"remotePort"`
	Up         int64  `json:"up"`
	Down       int64  `json:"down"`
	Used       int64  `json:"used"`
	UsedText   string `json:"usedText"`
	Enabled    bool   `json:"enabled"`
	DisabledBy string `json:"disabledBy"`
	ObservedAt int64  `json:"observedAt"`
	LastError  string `json:"lastError"`
}

func (s *ServerManagementService) NodeUsage(port int) ([]NodeUsage, error) {
	var account model.Inbound
	if err := database.GetDB().Where("port = ?", port).First(&account).Error; err != nil {
		return nil, err
	}
	nodes, err := s.Nodes()
	if err != nil {
		return nil, err
	}
	out := make([]NodeUsage, 0, len(nodes))
	for _, node := range nodes {
		if !node.Enabled {
			continue
		}
		var row model.NodeTraffic
		err := database.GetDB().Where("node_id = ? AND account_id = ?", node.Id, account.Id).First(&row).Error
		if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
		out = append(out, NodeUsage{NodeID: node.Id, Name: node.Name, RemotePort: row.Port, Up: row.Up, Down: row.Down, Used: row.Up + row.Down, UsedText: FormatTrafficSize(row.Up + row.Down), Enabled: row.Enabled, DisabledBy: row.DisabledBy, ObservedAt: row.ObservedAt, LastError: node.LastError})
	}
	return out, nil
}

func TrafficYyyymm(t time.Time) int { return t.Year()*100 + int(t.Month()) }
func YyyymmPeriod(month int) string { return fmt.Sprintf("%04d-%02d", month/100, month%100) }

// Traffic reads every enabled agent. Failed nodes retain their last known
// counters with a stale timestamp; a stale zero is never interpreted as proof
// that an account is under its shared limit.
func (s *ServerManagementService) Traffic() ([]*entity.ServerTraffic, error) {
	trafficMu.Lock()
	defer trafficMu.Unlock()
	nodes, err := s.Nodes()
	if err != nil {
		return nil, err
	}
	var failures []error
	for i := range nodes {
		node := &nodes[i]
		if !node.Enabled {
			continue
		}
		var reply struct {
			ObservedAt int64          `json:"observedAt"`
			Inbounds   []agentTraffic `json:"inbounds"`
		}
		if err := s.call(node, http.MethodGet, "/traffic", nil, "", &reply); err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", node.Name, err))
			_ = database.GetDB().Model(node).Update("last_error", err.Error()).Error
			continue
		}
		if err := database.GetDB().Transaction(func(tx *gorm.DB) error {
			if err := tx.Where("node_id = ?", node.Id).Delete(&model.NodeTraffic{}).Error; err != nil {
				return err
			}
			for _, in := range reply.Inbounds {
				if in.AccountID <= 0 || in.Port <= 0 || in.Up < 0 || in.Down < 0 {
					return errors.New("被控端返回了无效流量")
				}
				row := model.NodeTraffic{NodeID: node.Id, AccountID: in.AccountID, Port: in.Port, Up: in.Up, Down: in.Down, Enabled: in.Enabled, DisabledBy: in.DisabledBy, ManagerRevision: in.ManagerRevision, ObservedAt: time.Now().Unix()}
				if err := tx.Create(&row).Error; err != nil {
					return err
				}
			}
			return tx.Model(node).Updates(map[string]interface{}{"last_seen": time.Now().Unix(), "last_error": ""}).Error
		}); err != nil {
			failures = append(failures, fmt.Errorf("%s: %w", node.Name, err))
		} else if err := s.reconcileNode(node.Id, reply.Inbounds); err != nil {
			failures = append(failures, fmt.Errorf("%s 账号核对: %w", node.Name, err))
		}
	}
	if err := s.DispatchPending(); err != nil {
		failures = append(failures, err)
	}
	// A previous month's reset must finish before any new limit decision.
	current := TrafficYyyymm(time.Now().In(common.ShanghaiLocation))
	confirmed, err := s.confirmedMonth()
	if err != nil {
		return nil, err
	}
	if confirmed == current {
		if err := s.enforceLimits(); err != nil {
			failures = append(failures, err)
		}
	}
	traffic, err := s.GetTrafficCache()
	if err != nil {
		return nil, err
	}
	if len(failures) > 0 {
		return traffic, errors.Join(failures...)
	}
	return traffic, nil
}

func (s *ServerManagementService) GetTrafficCache() ([]*entity.ServerTraffic, error) {
	var accounts []model.Inbound
	if err := database.GetDB().Find(&accounts).Error; err != nil {
		return nil, err
	}
	portByID := map[int]int{}
	for _, in := range accounts {
		portByID[in.Id] = in.Port
	}
	var rows []model.NodeTraffic
	if err := database.GetDB().Find(&rows).Error; err != nil {
		return nil, err
	}
	by := map[int]*entity.ServerTraffic{}
	for _, row := range rows {
		port, exists := portByID[row.AccountID]
		if !exists {
			continue
		}
		v := by[port]
		if v == nil {
			v = &entity.ServerTraffic{Port: port, Enable: true}
			by[port] = v
		}
		v.Up += row.Up
		v.Down += row.Down
		v.Used += row.Up + row.Down
		v.Enable = v.Enable && row.Enabled
	}
	out := make([]*entity.ServerTraffic, 0, len(by))
	for _, v := range by {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Port < out[j].Port })
	return out, nil
}

func (s *ServerManagementService) ResetAccountTrafficCache(accountID int) {
	trafficMu.Lock()
	defer trafficMu.Unlock()
	_ = database.GetDB().Where("account_id = ?", accountID).Delete(&model.NodeTraffic{}).Error
}

func (s *ServerManagementService) ResetAccountTraffic(id int) error {
	trafficMu.Lock()
	defer trafficMu.Unlock()
	var account model.Inbound
	if err := database.GetDB().First(&account, id).Error; err != nil {
		return err
	}
	var nodes []model.ManagedNode
	if err := database.GetDB().Where("enabled = ?", true).Find(&nodes).Error; err != nil {
		return err
	}
	if err := database.GetDB().Transaction(func(tx *gorm.DB) error {
		for _, node := range nodes {
			if err := enqueue(tx, node.Id, &account, "reset", "manual-"+operationID()); err != nil {
				return err
			}
		}
		return nil
	}); err != nil {
		return err
	}
	if err := s.DispatchPending(); err != nil {
		return err
	}
	return database.GetDB().Model(&model.NodeTraffic{}).Where("account_id = ?", account.Id).UpdateColumns(map[string]interface{}{"up": 0, "down": 0}).Error
}

func (s *ServerManagementService) Summary(inboundID int) ([]*entity.TrafficSummary, error) {
	remote, err := s.GetTrafficCache()
	if err != nil {
		return nil, err
	}
	by := map[int]*entity.ServerTraffic{}
	for _, v := range remote {
		by[v.Port] = v
	}
	nodes, err := s.Nodes()
	if err != nil {
		return nil, err
	}
	var readings []model.NodeTraffic
	if err := database.GetDB().Find(&readings).Error; err != nil {
		return nil, err
	}
	perNode := make(map[int]map[int]model.NodeTraffic)
	for _, reading := range readings {
		if perNode[reading.NodeID] == nil {
			perNode[reading.NodeID] = map[int]model.NodeTraffic{}
		}
		perNode[reading.NodeID][reading.AccountID] = reading
	}
	setting, err := s.GetSetting()
	if err != nil {
		return nil, err
	}
	query := database.GetDB().Model(&model.Inbound{})
	if inboundID > 0 {
		query = query.Where("id = ?", inboundID)
	}
	var accounts []model.Inbound
	if err := query.Find(&accounts).Error; err != nil {
		return nil, err
	}
	out := make([]*entity.TrafficSummary, 0, len(accounts))
	for _, in := range accounts {
		var used int64
		remoteEnabled := true
		known := false
		if r := by[in.Port]; r != nil {
			used = r.Used
		}
		for _, node := range nodes {
			if !node.Enabled {
				continue
			}
			reading, exists := perNode[node.Id][in.Id]
			if !exists || reading.Port != in.Port || reading.ManagerRevision != inboundRevision(&in) || node.LastError != "" || time.Since(time.Unix(reading.ObservedAt, 0)) > time.Duration(setting.HeartbeatMinutes*2)*time.Minute {
				known = false
				break
			}
			known = true
			remoteEnabled = remoteEnabled && reading.Enabled
		}
		local := in.Up + in.Down
		over := TrafficOverlimit(local, used, in.Total)
		enabled := in.Enable && remoteEnabled
		status := TrafficStatusOf(enabled, over)
		if status == TrafficStatusEnabled && !known {
			status = "unknown"
		}
		out = append(out, &entity.TrafficSummary{InboundId: in.Id, Username: in.Remark, Port: in.Port,
			Local: local, Remote: used, Total: local + used, Limit: in.Total, Enable: enabled,
			MonthlyReset: in.MonthlyReset, Overlimit: over, Status: status,
			LocalText: FormatTrafficSize(local), RemoteText: FormatTrafficSize(used), TotalText: FormatTrafficSize(local + used), LimitText: FormatTrafficLimit(in.Total)})
	}
	return out, nil
}

func (s *ServerManagementService) enforceLimits() error {
	setting, err := s.GetSetting()
	if err != nil {
		return err
	}
	rows, err := s.Summary(0)
	if err != nil {
		return err
	}
	for _, row := range rows {
		var reason string
		var account model.Inbound
		if err := database.GetDB().First(&account, row.InboundId).Error; err != nil {
			return err
		}
		if account.ExpiryTime > 0 && account.ExpiryTime <= time.Now().UnixMilli() {
			reason = "expired"
		}
		if reason == "" && setting.AutoDisable && row.Overlimit {
			reason = "limit"
		}
		if reason == "" || (!account.Enable && account.DisabledBy == reason) {
			continue
		}
		if !account.Enable && account.DisabledBy == "manual" {
			continue
		}
		if err := database.GetDB().Model(&account).Updates(map[string]interface{}{"enable": false, "disabled_by": reason}).Error; err != nil {
			return err
		}
		account.Enable, account.DisabledBy = false, reason
		if err := s.queueInbound(&account, 0, "disable"); err != nil {
			return err
		}
	}
	return nil
}

func (s *ServerManagementService) confirmedMonth() (int, error) {
	var row model.Setting
	err := database.GetDB().Where("key = ?", managerMonthKey).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(row.Value)
}

func (s *ServerManagementService) MaybeMonthlyReset() error {
	trafficMu.Lock()
	defer trafficMu.Unlock()
	managerMu.Lock()
	now := time.Now().In(common.ShanghaiLocation)
	month := TrafficYyyymm(now)
	confirmed, err := s.confirmedMonth()
	if err != nil {
		managerMu.Unlock()
		return err
	}
	if confirmed == 0 {
		err = setValue(database.GetDB(), managerMonthKey, strconv.Itoa(month))
		managerMu.Unlock()
		return err
	}
	if confirmed >= month {
		managerMu.Unlock()
		return nil
	}
	period := confirmed
	var accounts []model.Inbound
	var nodes []model.ManagedNode
	if err = database.GetDB().Where("monthly_reset = ?", true).Find(&accounts).Error; err == nil {
		err = database.GetDB().Where("enabled = ?", true).Find(&nodes).Error
	}
	if err == nil {
		err = database.GetDB().Transaction(func(tx *gorm.DB) error {
			for _, in := range accounts {
				for _, node := range nodes {
					operation := fmt.Sprintf("reset-%d-%d-%d", period, node.Id, in.Id)
					var count int64
					if err := tx.Model(&model.SyncTask{}).Where("operation_id = ?", operation).Count(&count).Error; err != nil {
						return err
					}
					if count == 0 {
						if err := enqueue(tx, node.Id, &in, "reset", operation); err != nil {
							return err
						}
					}
				}
			}
			return nil
		})
	}
	managerMu.Unlock()
	if err != nil {
		return err
	}
	if err := s.DispatchPending(); err != nil {
		return err
	}
	var remaining int64
	if err := database.GetDB().Model(&model.SyncTask{}).Where("kind = ? AND status = ? AND operation_id LIKE ?", "reset", "pending", fmt.Sprintf("reset-%d-%%", period)).Count(&remaining).Error; err != nil {
		return err
	}
	if remaining > 0 {
		return fmt.Errorf("仍有 %d 个被控端月度重置任务待完成", remaining)
	}
	return database.GetDB().Transaction(func(tx *gorm.DB) error {
		for _, in := range accounts {
			var snapshots []model.NodeTrafficSnapshot
			if err := tx.Where("account_id = ? AND yyyymm = ?", in.Id, period).Find(&snapshots).Error; err != nil {
				return err
			}
			var up, down int64
			for _, snapshot := range snapshots {
				up += snapshot.Up
				down += snapshot.Down
			}
			var existing int64
			if err := tx.Model(&model.TrafficSnapshot{}).Where("inbound_id = ? AND yyyymm = ?", in.Id, period).Count(&existing).Error; err != nil {
				return err
			}
			if existing == 0 {
				snap := model.TrafficSnapshot{InboundId: in.Id, Yyyymm: period, Port: in.Port, Remark: in.Remark, LocalUp: in.Up, LocalDown: in.Down, RemoteUp: up, RemoteDown: down, Total: in.Total, ResetAt: now.Unix()}
				if err := tx.Create(&snap).Error; err != nil {
					return err
				}
			}
			if err := tx.Model(&model.Inbound{}).Where("id = ?", in.Id).UpdateColumns(map[string]interface{}{"up": 0, "down": 0}).Error; err != nil {
				return err
			}
			if err := tx.Model(&model.NodeTraffic{}).Where("account_id = ?", in.Id).UpdateColumns(map[string]interface{}{"up": 0, "down": 0}).Error; err != nil {
				return err
			}
			if in.DisabledBy == "limit" {
				in.Enable, in.DisabledBy = true, ""
				if err := tx.Model(&model.Inbound{}).Where("id = ?", in.Id).Updates(map[string]interface{}{"enable": true, "disabled_by": ""}).Error; err != nil {
					return err
				}
				for _, node := range nodes {
					if err := enqueue(tx, node.Id, &in, "upsert", ""); err != nil {
						return err
					}
				}
			}
		}
		return setValue(tx, managerMonthKey, strconv.Itoa(nextMonth(period)))
	})
}

func (s *ServerManagementService) TrafficResetSnapshots(inboundID int) ([]*entity.TrafficSnapshot, error) {
	query := database.GetDB().Model(&model.TrafficSnapshot{})
	if inboundID > 0 {
		query = query.Where("inbound_id = ?", inboundID)
	}
	var rows []model.TrafficSnapshot
	if err := query.Order("yyyymm DESC, port").Find(&rows).Error; err != nil {
		return nil, err
	}
	out := make([]*entity.TrafficSnapshot, 0, len(rows))
	for _, row := range rows {
		local, remote := row.LocalUp+row.LocalDown, row.RemoteUp+row.RemoteDown
		out = append(out, &entity.TrafficSnapshot{Yyyymm: row.Yyyymm, Period: YyyymmPeriod(row.Yyyymm), InboundId: row.InboundId, Port: row.Port, Remark: row.Remark, Local: local, Remote: remote, Used: local + remote, Limit: row.Total, LocalText: FormatTrafficSize(local), RemoteText: FormatTrafficSize(remote), UsedText: FormatTrafficSize(local + remote), LimitText: FormatTrafficLimit(row.Total), ResetAt: row.ResetAt})
	}
	return out, nil
}

// RemoteInbounds reads every enabled node for subscription generation.
func (s *ServerManagementService) RemoteInbounds(port int) ([]map[string]interface{}, error) {
	nodes, err := s.Nodes()
	if err != nil {
		return nil, err
	}
	out := make([]map[string]interface{}, 0, len(nodes))
	for _, node := range nodes {
		if !node.Enabled {
			continue
		}
		var inbound model.Inbound
		if err := s.call(&node, http.MethodGet, "/inbounds/"+strconv.Itoa(port), nil, "", &inbound); err != nil {
			return nil, fmt.Errorf("%s: %w", node.Name, err)
		}
		b, err := json.Marshal(inbound)
		if err != nil {
			return nil, err
		}
		var item map[string]interface{}
		if err := json.Unmarshal(b, &item); err != nil {
			return nil, err
		}
		item["remoteName"], item["remoteAddress"] = node.Name, node.Address
		out = append(out, item)
	}
	return out, nil
}
