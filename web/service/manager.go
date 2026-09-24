package service

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"xui/database"
	"xui/database/model"
	"xui/web/entity"

	"gorm.io/gorm"
)

const managerSettingKey = "managerSettings"
const managerMonthKey = "managerConfirmedMonth"

const syncAttemptTimeout = 30 * time.Second
const syncRetryInterval = 6 * time.Hour
const syncMaxRetries = 12

var managerMu sync.Mutex

type ServerManagementService struct{}

type agentHTTPError struct {
	StatusCode int
	Message    string
}

func (e *agentHTTPError) Error() string {
	return fmt.Sprintf("被控端 HTTP %d: %s", e.StatusCode, e.Message)
}

type NodeInput struct {
	ID         int    `json:"id" form:"id"`
	Name       string `json:"name" form:"name"`
	URL        string `json:"url" form:"url"`
	Address    string `json:"address" form:"address"`
	Token      string `json:"token" form:"token"`
	CertSHA256 string `json:"certSha256" form:"certSha256"`
	Enabled    bool   `json:"enabled" form:"enabled"`
}

type agentTraffic struct {
	AccountID       int    `json:"accountId"`
	Port            int    `json:"port"`
	Up              int64  `json:"up"`
	Down            int64  `json:"down"`
	Enabled         bool   `json:"enabled"`
	DisabledBy      string `json:"disabledBy"`
	ManagerRevision string `json:"managerRevision"`
}

func (s *ServerManagementService) reconcileNode(nodeID int, observed []agentTraffic) error {
	managerMu.Lock()
	defer managerMu.Unlock()
	var desired []model.Inbound
	if err := database.GetDB().Find(&desired).Error; err != nil {
		return err
	}
	byID := make(map[int]model.Inbound, len(desired))
	for _, in := range desired {
		byID[in.Id] = in
	}
	seen := make(map[int]agentTraffic, len(observed))
	for _, row := range observed {
		seen[row.AccountID] = row
	}
	queueIfMissing := func(in *model.Inbound, kind string) error {
		var pending int64
		if err := database.GetDB().Model(&model.SyncTask{}).Where("node_id = ? AND account_id = ? AND status = ?", nodeID, in.Id, "pending").Count(&pending).Error; err != nil {
			return err
		}
		if pending > 0 {
			return nil
		}
		return enqueue(database.GetDB(), nodeID, in, kind, "")
	}
	for _, in := range desired {
		actual, exists := seen[in.Id]
		if !exists || actual.Port != in.Port || actual.ManagerRevision != inboundRevision(&in) {
			if err := queueIfMissing(&in, "upsert"); err != nil {
				return err
			}
		}
	}
	for _, actual := range observed {
		if _, exists := byID[actual.AccountID]; !exists {
			stale := model.Inbound{Id: actual.AccountID, Port: actual.Port}
			if err := queueIfMissing(&stale, "delete"); err != nil {
				return err
			}
		}
	}
	return nil
}

func operationID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

func inboundRevision(in *model.Inbound) string {
	data, _ := json.Marshal(struct {
		ID             int
		Port           int
		Protocol       model.Protocol
		Settings       string
		StreamSettings string
		Sniffing       string
		Remark         string
		Total          int64
		ExpiryTime     int64
		MonthlyReset   bool
		Enable         bool
		DisabledBy     string
	}{in.Id, in.Port, in.Protocol, in.Settings, in.StreamSettings, in.Sniffing, in.Remark, in.Total, in.ExpiryTime, in.MonthlyReset, in.Enable, in.DisabledBy})
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// hostOnlyAddress 把订阅地址归一成纯主机名：去掉 http:// / https:// 协议前缀、
// 路径与端口。客户端连接用的端口由各入站自己决定，生成链接时会另拼 ":入站端口"，
// 地址带端口会拼成 host:port:port 这类非法地址。
// 值可能来自被控端面板或浏览器地址栏，两者都容易带上协议前缀，这里一并剥掉。
func hostOnlyAddress(address string) string {
	address = strings.TrimSpace(address)
	if i := strings.Index(address, "://"); i >= 0 {
		address = address[i+3:]
	}
	if i := strings.IndexAny(address, "/?#"); i >= 0 {
		address = address[:i]
	}
	i := strings.LastIndex(address, ":")
	if i <= 0 || strings.Contains(address[:i], ":") {
		return address
	}
	if _, err := strconv.Atoi(address[i+1:]); err != nil {
		return address
	}
	return address[:i]
}

func (s *ServerManagementService) Nodes() ([]model.ManagedNode, error) {
	var nodes []model.ManagedNode
	err := database.GetDB().Order("id").Find(&nodes).Error
	return nodes, err
}

func (s *ServerManagementService) SaveNode(input NodeInput) error {
	u, err := url.Parse(strings.TrimSpace(input.URL))
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return errors.New("API 地址必须是 HTTPS 站点根地址")
	}
	if strings.TrimSpace(input.Name) == "" || strings.TrimSpace(input.Address) == "" {
		return errors.New("节点名称和订阅地址不能为空")
	}
	pin := strings.ToLower(strings.ReplaceAll(strings.TrimSpace(input.CertSHA256), ":", ""))
	if pin != "" {
		if _, err := hex.DecodeString(pin); err != nil || len(pin) != 64 {
			return errors.New("证书 SHA256 指纹必须是 64 位十六进制")
		}
	}
	managerMu.Lock()
	defer managerMu.Unlock()
	node := model.ManagedNode{}
	if input.ID > 0 {
		if err := database.GetDB().First(&node, input.ID).Error; err != nil {
			return err
		}
	}
	if input.Token != "" {
		node.Token = input.Token
	}
	if len(node.Token) < 32 {
		return errors.New("API 令牌至少 32 字符")
	}
	newURL := strings.TrimRight(u.String(), "/")
	if input.ID > 0 && node.URL != newURL {
		return errors.New("已有节点的 API 地址不能修改；请先删除旧节点并确认远端账号已清理，再添加新节点")
	}
	oldEnabled := node.Enabled
	node.Name, node.URL, node.Address, node.CertSHA256, node.Enabled = strings.TrimSpace(input.Name), newURL, hostOnlyAddress(input.Address), pin, input.Enabled
	if node.Enabled {
		var capability struct {
			APIVersion int `json:"apiVersion"`
		}
		if err := s.call(&node, http.MethodGet, "/capabilities", nil, "", &capability); err != nil {
			return fmt.Errorf("测试被控端连接失败: %w", err)
		}
		if capability.APIVersion != 1 {
			return fmt.Errorf("被控端 API 版本 %d 不受支持", capability.APIVersion)
		}
	}
	if input.ID > 0 {
		var previous model.ManagedNode
		if err := database.GetDB().First(&previous, input.ID).Error; err != nil {
			return err
		}
		if previous.Name != node.Name {
			if err := preserveHistoricalNodeName(database.GetDB(), previous.Id, previous.Name); err != nil {
				return err
			}
		}
	}
	if err := database.GetDB().Save(&node).Error; err != nil {
		return err
	}
	// A newly enabled node receives every desired account, including accounts
	// created before it joined the manager.
	if node.Enabled && (input.ID == 0 || !oldEnabled) {
		var accounts []model.Inbound
		if err := database.GetDB().Find(&accounts).Error; err != nil {
			return err
		}
		for i := range accounts {
			if err := enqueue(database.GetDB(), node.Id, &accounts[i], "upsert", ""); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *ServerManagementService) DeleteNode(id int) error {
	managerMu.Lock()
	defer managerMu.Unlock()
	var node model.ManagedNode
	if err := database.GetDB().First(&node, id).Error; err != nil {
		return err
	}
	var pending int64
	if err := database.GetDB().Model(&model.SyncTask{}).Where("node_id = ? AND status = ?", id, "pending").Count(&pending).Error; err != nil {
		return err
	}
	if pending > 0 {
		return fmt.Errorf("该节点仍有 %d 个待同步任务，请先修复连接并等待重试", pending)
	}
	var listed struct {
		Inbounds []model.Inbound `json:"inbounds"`
	}
	if err := s.call(&node, http.MethodGet, "/inbounds", nil, "", &listed); err != nil {
		return fmt.Errorf("读取被控端账号失败: %w", err)
	}
	for _, in := range listed.Inbounds {
		if err := s.call(&node, http.MethodDelete, "/inbounds/"+strconv.Itoa(in.Port), map[string]int{"accountId": in.ManagerAccountID}, operationID(), nil); err != nil {
			return fmt.Errorf("删除被控端端口 %d 失败，节点仍保留在管理端: %w", in.Port, err)
		}
	}
	return database.GetDB().Transaction(func(tx *gorm.DB) error {
		if err := preserveHistoricalNodeName(tx, id, node.Name); err != nil {
			return err
		}
		if err := tx.Where("node_id = ?", id).Delete(&model.SyncTask{}).Error; err != nil {
			return err
		}
		if err := tx.Where("node_id = ?", id).Delete(&model.NodeTraffic{}).Error; err != nil {
			return err
		}
		return tx.Delete(&model.ManagedNode{}, id).Error
	})
}

// ForceDeleteNode is an explicit offline detach. It cannot discard an
// unfinished monthly reset because that would silently falsify the archive.
func (s *ServerManagementService) ForceDeleteNode(id int) error {
	managerMu.Lock()
	defer managerMu.Unlock()
	var node model.ManagedNode
	if err := database.GetDB().First(&node, id).Error; err != nil {
		return err
	}
	var monthly int64
	if err := database.GetDB().Model(&model.SyncTask{}).Where("node_id = ? AND kind = ? AND status = ?", id, "reset", "pending").Count(&monthly).Error; err != nil {
		return err
	}
	if monthly > 0 {
		return fmt.Errorf("该节点仍有 %d 个未完成的月度重置任务，无法强制移除；请恢复节点连接并完成结算", monthly)
	}
	return database.GetDB().Transaction(func(tx *gorm.DB) error {
		if err := preserveHistoricalNodeName(tx, id, node.Name); err != nil {
			return err
		}
		if err := tx.Where("node_id = ?", id).Delete(&model.SyncTask{}).Error; err != nil {
			return err
		}
		if err := tx.Where("node_id = ?", id).Delete(&model.NodeTraffic{}).Error; err != nil {
			return err
		}
		return tx.Delete(&model.ManagedNode{}, id).Error
	})
}

func preserveHistoricalNodeName(tx *gorm.DB, nodeID int, name string) error {
	return tx.Model(&model.NodeTrafficSnapshot{}).
		Where("node_id = ? AND (node_name = '' OR node_name IS NULL)", nodeID).
		Update("node_name", name).Error
}

func (s *ServerManagementService) GetSetting() (*entity.ServerSetting, error) {
	setting := &entity.ServerSetting{AutoDisable: true, HeartbeatMinutes: 10}
	var row model.Setting
	err := database.GetDB().Where("key = ?", managerSettingKey).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return setting, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal([]byte(row.Value), setting); err != nil {
		return nil, err
	}
	if setting.HeartbeatMinutes < 5 {
		setting.HeartbeatMinutes = 5
	}
	return setting, nil
}

func (s *ServerManagementService) SaveSetting(setting *entity.ServerSetting) error {
	if setting.HeartbeatMinutes < 5 {
		return errors.New("心跳间隔不能低于 5 分钟")
	}
	b, err := json.Marshal(entity.ServerSetting{AutoDisable: setting.AutoDisable, HeartbeatMinutes: setting.HeartbeatMinutes})
	if err != nil {
		return err
	}
	return setValue(database.GetDB(), managerSettingKey, string(b))
}

func setValue(db *gorm.DB, key, value string) error {
	var row model.Setting
	err := db.Where("key = ?", key).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return db.Create(&model.Setting{Key: key, Value: value}).Error
	}
	if err != nil {
		return err
	}
	return db.Model(&row).Update("value", value).Error
}

func (s *ServerManagementService) client(node *model.ManagedNode) (*http.Client, error) {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	if node.CertSHA256 != "" {
		expected, err := hex.DecodeString(node.CertSHA256)
		if err != nil || len(expected) != sha256.Size {
			return nil, errors.New("无效的证书指纹")
		}
		// A pinned self-signed certificate is authenticated by its exact SHA256.
		transport.TLSClientConfig.InsecureSkipVerify = true
		transport.TLSClientConfig.VerifyConnection = func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 {
				return errors.New("被控端没有提供证书")
			}
			cert := state.PeerCertificates[0]
			actual := sha256.Sum256(cert.Raw)
			if subtle.ConstantTimeCompare(actual[:], expected) != 1 {
				return errors.New("被控端证书指纹不匹配")
			}
			if time.Now().Before(cert.NotBefore) || time.Now().After(cert.NotAfter) {
				return errors.New("被控端证书已失效")
			}
			return nil
		}
	}
	return &http.Client{Transport: transport, Timeout: syncAttemptTimeout}, nil
}

func (s *ServerManagementService) call(node *model.ManagedNode, method, path string, payload interface{}, operation string, out interface{}) error {
	return s.callContext(context.Background(), node, method, path, payload, operation, out)
}

func (s *ServerManagementService) callContext(ctx context.Context, node *model.ManagedNode, method, path string, payload interface{}, operation string, out interface{}) error {
	client, err := s.client(node)
	if err != nil {
		return err
	}
	var body io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, node.URL+"/api/v1"+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+node.Token)
	req.Header.Set("Content-Type", "application/json")
	if operation != "" {
		req.Header.Set("Idempotency-Key", operation)
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	limited := io.LimitReader(resp.Body, 1<<20)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var failure struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(limited).Decode(&failure)
		return &agentHTTPError{StatusCode: resp.StatusCode, Message: failure.Error}
	}
	if out != nil {
		return json.NewDecoder(limited).Decode(out)
	}
	return nil
}

func enqueue(tx *gorm.DB, nodeID int, inbound *model.Inbound, kind, operation string) error {
	if operation == "" {
		operation = operationID()
	}
	var payload []byte
	var err error
	switch kind {
	case "upsert", "enable":
		copy := *inbound
		copy.ManagerAccountID = inbound.Id
		copy.ManagerRevision = inboundRevision(inbound)
		payload, err = json.Marshal(copy)
	case "delete":
		payload, err = json.Marshal(map[string]interface{}{"accountId": inbound.Id})
	case "disable":
		payload, err = json.Marshal(map[string]interface{}{"accountId": inbound.Id, "reason": inbound.DisabledBy, "managerRevision": inboundRevision(inbound)})
	case "reset":
		period := 0
		if strings.HasPrefix(operation, "reset-") {
			period, _ = strconv.Atoi(strings.Split(operation, "-")[1])
		}
		payload, err = json.Marshal(map[string]interface{}{"accountId": inbound.Id, "period": period, "operationId": operation})
	default:
		return errors.New("unknown sync task")
	}
	if err != nil {
		return err
	}
	if kind == "upsert" {
		// A failed, older configuration must not keep an account stuck ahead of
		// its correction. Refresh every pending upsert in place so its position
		// relative to reset/disable tasks is preserved.
		var pending []model.SyncTask
		if err := tx.Where("node_id = ? AND account_id = ? AND kind = ? AND status = ?", nodeID, inbound.Id, "upsert", "pending").Find(&pending).Error; err != nil {
			return err
		}
		for _, task := range pending {
			if err := tx.Model(&task).Updates(map[string]interface{}{
				// 刷新负载但不重置 attempts：否则失败计数每次配置变更都被清零，
				// 持续故障的节点永远不会到达 syncMaxRetries 被标记 abandoned。
				"port": inbound.Port, "payload": string(payload), "error": "", "next_retry_at": 0,
			}).Error; err != nil {
				return err
			}
		}
		if len(pending) > 0 {
			var latest model.SyncTask
			if err := tx.Where("node_id = ? AND account_id = ? AND status = ?", nodeID, inbound.Id, "pending").Order("id DESC").First(&latest).Error; err != nil {
				return err
			}
			if latest.Kind == "upsert" {
				return nil
			}
		}
	}
	return tx.Create(&model.SyncTask{NodeID: nodeID, AccountID: inbound.Id, Port: inbound.Port, Kind: kind, Payload: string(payload), OperationID: operation, Status: "pending", CreatedAt: time.Now().Unix()}).Error
}

func (s *ServerManagementService) queueInbound(inbound *model.Inbound, oldPort int, kind string) error {
	managerMu.Lock()
	var nodes []model.ManagedNode
	nodeQuery := database.GetDB()
	if kind != "delete" {
		nodeQuery = nodeQuery.Where("enabled = ?", true)
	}
	err := nodeQuery.Find(&nodes).Error
	if err == nil {
		err = database.GetDB().Transaction(func(tx *gorm.DB) error {
			if kind == "delete" {
				// A delayed manual enable must never recreate a deleted account.
				if err := tx.Model(&model.SyncTask{}).Where("account_id = ? AND status = ?", inbound.Id, "pending").
					Updates(map[string]interface{}{"status": "abandoned", "error": "账号已删除"}).Error; err != nil {
					return err
				}
			}
			for _, node := range nodes {
				// The agent finds an existing row by manager_account_id. Its
				// update changes the port in place and preserves traffic counters.
				if err := enqueue(tx, node.Id, inbound, kind, ""); err != nil {
					return err
				}
			}
			return nil
		})
	}
	managerMu.Unlock()
	if err != nil {
		return err
	}
	return s.DispatchPending()
}

func (s *ServerManagementService) SyncInbound(inbound *model.Inbound, oldPort int, create bool) error {
	return s.queueInbound(inbound, oldPort, "upsert")
}

func (s *ServerManagementService) DeleteSyncedInbound(inbound *model.Inbound) error {
	return s.queueInbound(inbound, 0, "delete")
}

func (s *ServerManagementService) DispatchPending() error {
	managerMu.Lock()
	defer managerMu.Unlock()
	var tasks []model.SyncTask
	// Filter inactive nodes before LIMIT: skipped tasks must not occupy the
	// first page forever. Explicit resets and their enable steps still run.
	if err := database.GetDB().Model(&model.SyncTask{}).
		Joins("JOIN managed_nodes ON managed_nodes.id = sync_tasks.node_id").
		Where("sync_tasks.status = ? AND sync_tasks.next_retry_at <= ? AND (managed_nodes.enabled = ? OR sync_tasks.kind IN ?) AND NOT EXISTS (SELECT 1 FROM sync_tasks earlier WHERE earlier.node_id = sync_tasks.node_id AND earlier.account_id = sync_tasks.account_id AND earlier.status = ? AND earlier.id < sync_tasks.id AND (managed_nodes.enabled = ? OR earlier.kind IN ?))", "pending", time.Now().Unix(), true, []string{"reset", "enable"}, "pending", true, []string{"reset", "enable"}).
		Order("sync_tasks.id").Limit(500).Find(&tasks).Error; err != nil {
		return err
	}
	type accountKey struct{ nodeID, accountID int }
	blocked := map[accountKey]bool{}
	var failures []string
	for _, task := range tasks {
		key := accountKey{task.NodeID, task.AccountID}
		if blocked[key] {
			continue
		}
		var node model.ManagedNode
		if err := database.GetDB().First(&node, task.NodeID).Error; err != nil {
			continue
		}
		// Existing monthly reset tasks remain payable even after the node is
		// disabled for future account synchronization.
		if !node.Enabled && task.Kind != "reset" && task.Kind != "enable" {
			continue
		}
		if task.Kind == "enable" {
			var desired model.Inbound
			err := database.GetDB().First(&desired, task.AccountID).Error
			if errors.Is(err, gorm.ErrRecordNotFound) || (err == nil && !desired.Enable && desired.DisabledBy != "limit") {
				if err := database.GetDB().Model(&task).Updates(map[string]interface{}{"status": "abandoned", "error": "账号已删除或被管理员停用"}).Error; err != nil {
					return err
				}
				continue
			}
			if err != nil {
				return err
			}
			copy := desired
			copy.Enable, copy.DisabledBy = true, ""
			copy.ManagerAccountID = desired.Id
			copy.ManagerRevision = inboundRevision(&copy)
			payload, err := json.Marshal(copy)
			if err != nil {
				return err
			}
			task.Port, task.Payload = desired.Port, string(payload)
			if err := database.GetDB().Model(&task).Updates(map[string]interface{}{"port": task.Port, "payload": task.Payload}).Error; err != nil {
				return err
			}
		}
		method, path := "PUT", "/inbounds/"+strconv.Itoa(task.Port)
		switch task.Kind {
		case "delete":
			method = "DELETE"
		case "disable":
			method, path = "POST", path+"/disable"
		case "reset":
			method, path = "POST", path+"/traffic/reset"
		}
		ctx, cancel := context.WithTimeout(context.Background(), syncAttemptTimeout)
		var result json.RawMessage
		err := s.callContext(ctx, &node, method, path, json.RawMessage(task.Payload), task.OperationID, &result)
		if task.Kind == "reset" && strings.HasPrefix(task.OperationID, "manual-") && err != nil {
			var remoteErr *agentHTTPError
			if errors.As(err, &remoteErr) && remoteErr.StatusCode == http.StatusNotFound {
				// A node may have been disabled before this account was first
				// synchronized. Create the current account, then reset it.
				var desired model.Inbound
				if err = database.GetDB().First(&desired, task.AccountID).Error; err == nil {
					copy := desired
					copy.ManagerAccountID = desired.Id
					copy.ManagerRevision = inboundRevision(&desired)
					err = s.callContext(ctx, &node, http.MethodPut, "/inbounds/"+strconv.Itoa(desired.Port), &copy, task.OperationID, nil)
					if err == nil {
						path = "/inbounds/" + strconv.Itoa(desired.Port) + "/traffic/reset"
						if desired.Port != task.Port {
							err = database.GetDB().Model(&task).Update("port", desired.Port).Error
						}
					}
					if err == nil {
						err = s.callContext(ctx, &node, method, path, json.RawMessage(task.Payload), task.OperationID, &result)
					}
				}
			}
		}
		if task.Kind == "disable" && err != nil {
			var remoteErr *agentHTTPError
			if errors.As(err, &remoteErr) && remoteErr.StatusCode == http.StatusNotFound {
				// A missing row must not acknowledge a disable. Recreate the
				// current desired account state before completing this task.
				var desired model.Inbound
				lookupErr := database.GetDB().First(&desired, task.AccountID).Error
				if errors.Is(lookupErr, gorm.ErrRecordNotFound) {
					err = nil // The account was deleted locally as well.
				} else if lookupErr != nil {
					err = lookupErr
				} else {
					copy := desired
					copy.ManagerAccountID = desired.Id
					copy.ManagerRevision = inboundRevision(&desired)
					err = s.callContext(ctx, &node, http.MethodPut, "/inbounds/"+strconv.Itoa(desired.Port), &copy, task.OperationID, &result)
				}
			}
		}
		cancel()
		if err == nil && task.Kind == "reset" && strings.HasPrefix(task.OperationID, "reset-") {
			var usage struct {
				Up   int64 `json:"up"`
				Down int64 `json:"down"`
			}
			if err = json.Unmarshal(result, &usage); err == nil {
				period, _ := strconv.Atoi(strings.Split(task.OperationID, "-")[1])
				snap := model.NodeTrafficSnapshot{NodeID: task.NodeID, NodeName: node.Name, AccountID: task.AccountID, Yyyymm: period, Port: task.Port, Up: usage.Up, Down: usage.Down, ResetAt: time.Now().Unix()}
				err = database.GetDB().Transaction(func(tx *gorm.DB) error {
					if err := tx.Where("node_id = ? AND account_id = ? AND yyyymm = ?", snap.NodeID, snap.AccountID, snap.Yyyymm).FirstOrCreate(&snap).Error; err != nil {
						return err
					}
					var total struct{ Up, Down int64 }
					if err := tx.Model(&model.NodeTrafficSnapshot{}).Where("account_id = ? AND yyyymm = ?", task.AccountID, period).
						Select("COALESCE(SUM(up), 0) AS up, COALESCE(SUM(down), 0) AS down").Scan(&total).Error; err != nil {
						return err
					}
					return tx.Model(&model.TrafficSnapshot{}).Where("inbound_id = ? AND yyyymm = ?", task.AccountID, period).
						Updates(map[string]interface{}{"remote_up": total.Up, "remote_down": total.Down}).Error
				})
			}
		}
		if err == nil && task.Kind == "reset" && strings.HasPrefix(task.OperationID, "manual-") {
			// The traffic poll runs before dispatch. Clear its pre-reset cache
			// after this node acknowledges the reset so the old value cannot
			// immediately trigger a new shared-limit disable.
			err = database.GetDB().Model(&model.NodeTraffic{}).
				Where("node_id = ? AND account_id = ?", task.NodeID, task.AccountID).
				UpdateColumns(map[string]interface{}{"up": 0, "down": 0}).Error
		}
		if err != nil {
			blocked[key] = true
			failures = append(failures, fmt.Sprintf("%s: %v", node.Name, err))
			attempts := task.Attempts + 1
			status, nextRetry := "pending", time.Now().Add(syncRetryInterval).Unix()
			if attempts > syncMaxRetries {
				status, nextRetry = "abandoned", int64(0)
			}
			if updateErr := database.GetDB().Model(&task).Updates(map[string]interface{}{"status": status, "error": err.Error(), "attempts": attempts, "next_retry_at": nextRetry}).Error; updateErr != nil {
				return updateErr
			}
			if status == "abandoned" && task.Kind == "reset" && strings.HasPrefix(task.OperationID, "manual-") {
				paired := "manual-enable-" + strings.TrimPrefix(task.OperationID, "manual-")
				if updateErr := database.GetDB().Model(&model.SyncTask{}).Where("operation_id = ? AND status = ?", paired, "pending").Updates(map[string]interface{}{"status": "abandoned", "error": "对应的手动重置已放弃"}).Error; updateErr != nil {
					return updateErr
				}
			}
			_ = database.GetDB().Model(&node).Updates(map[string]interface{}{"last_error": err.Error()}).Error
			continue
		}
		if err = database.GetDB().Model(&task).Updates(map[string]interface{}{"status": "done", "error": "", "attempts": task.Attempts + 1}).Error; err != nil {
			return err
		}
		if task.Kind == "reset" && strings.HasPrefix(task.OperationID, "manual-") {
			if err := s.finishManualReset(task.AccountID); err != nil {
				return err
			}
		}
		_ = database.GetDB().Model(&node).Updates(map[string]interface{}{"last_error": "", "last_seen": time.Now().Unix()}).Error
	}
	if len(failures) > 0 {
		return errors.New(strings.Join(failures, "; "))
	}
	return nil
}

func (s *ServerManagementService) finishManualReset(accountID int) error {
	var pending int64
	if err := database.GetDB().Model(&model.SyncTask{}).
		Where("account_id = ? AND kind = ? AND status = ? AND operation_id LIKE ?", accountID, "reset", "pending", "manual-%").
		Count(&pending).Error; err != nil {
		return err
	}
	if pending > 0 {
		return nil
	}
	// A stale pre-reset cache can have disabled the manager account while an
	// offline node was being retried. Restore only automatic limit disables.
	return database.GetDB().Model(&model.Inbound{}).
		Where("id = ? AND disabled_by = ?", accountID, "limit").
		Updates(map[string]interface{}{"enable": true, "disabled_by": ""}).Error
}

func (s *ServerManagementService) PendingTasks() ([]model.SyncTask, error) {
	var tasks []model.SyncTask
	err := database.GetDB().Where("status IN ?", []string{"pending", "abandoned"}).Order("id DESC").Find(&tasks).Error
	return tasks, err
}
