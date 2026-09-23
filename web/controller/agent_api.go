package controller

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"x-ui/database"
	"x-ui/database/model"
	"x-ui/web/service"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// AgentAPI is a machine interface. It deliberately does not share the browser
// session or the panel's administrator password.
type AgentAPI struct {
	inbounds service.InboundService
}

var agentMutationMu sync.Mutex

func (a *AgentAPI) ready(c *gin.Context) bool {
	agentMutationMu.Lock()
	defer agentMutationMu.Unlock()
	if err := service.EnsureManagedXray(); err != nil {
		apiError(c, err)
		return false
	}
	return true
}

func RegisterAgentAPI(engine *gin.Engine) {
	a := &AgentAPI{}
	g := engine.Group("/api/v1")
	g.Use(func(c *gin.Context) {
		secret := os.Getenv("XUI_AGENT_TOKEN")
		provided := strings.TrimPrefix(c.GetHeader("Authorization"), "Bearer ")
		if c.Request.TLS == nil || len(secret) < 32 || len(provided) != len(secret) || subtle.ConstantTimeCompare([]byte(provided), []byte(secret)) != 1 {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}
		c.Next()
	})
	g.GET("/capabilities", func(c *gin.Context) {
		c.JSON(200, gin.H{"apiVersion": 1, "features": []string{"inbounds.read", "inbounds.upsert", "inbounds.delete", "traffic.read", "traffic.reset", "inbounds.disable"}})
	})
	g.GET("/inbounds/:port", a.getInbound)
	g.GET("/inbounds", a.listInbounds)
	g.GET("/traffic", a.traffic)
	g.PUT("/inbounds/:port", a.upsert)
	g.DELETE("/inbounds/:port", a.remove)
	g.POST("/inbounds/:port/disable", a.disable)
	g.POST("/inbounds/:port/traffic/reset", a.reset)
}

func apiPort(c *gin.Context) (int, bool) {
	p, err := strconv.Atoi(c.Param("port"))
	if err != nil || p < 1 || p > 65535 {
		c.JSON(400, gin.H{"error": "invalid port"})
		return 0, false
	}
	return p, true
}

func apiError(c *gin.Context, err error) {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(404, gin.H{"error": "inbound not found"})
	} else {
		c.JSON(500, gin.H{"error": err.Error()})
	}
}

func (a *AgentAPI) getInbound(c *gin.Context) {
	if !a.ready(c) {
		return
	}
	port, ok := apiPort(c)
	if !ok {
		return
	}
	var in model.Inbound
	if err := database.GetDB().Where("port = ? AND manager_account_id > 0", port).First(&in).Error; err != nil {
		apiError(c, err)
		return
	}
	c.JSON(200, in)
}

func (a *AgentAPI) listInbounds(c *gin.Context) {
	if !a.ready(c) {
		return
	}
	var rows []model.Inbound
	if err := database.GetDB().Where("manager_account_id > 0").Find(&rows).Error; err != nil {
		apiError(c, err)
		return
	}
	c.JSON(200, gin.H{"inbounds": rows})
}

func (a *AgentAPI) traffic(c *gin.Context) {
	if !a.ready(c) {
		return
	}
	var rows []model.Inbound
	if err := database.GetDB().Where("manager_account_id > 0").Find(&rows).Error; err != nil {
		apiError(c, err)
		return
	}
	type item struct {
		AccountID       int    `json:"accountId"`
		Port            int    `json:"port"`
		Up              int64  `json:"up"`
		Down            int64  `json:"down"`
		Enabled         bool   `json:"enabled"`
		DisabledBy      string `json:"disabledBy"`
		ManagerRevision string `json:"managerRevision"`
	}
	out := make([]item, 0, len(rows))
	for _, row := range rows {
		out = append(out, item{row.ManagerAccountID, row.Port, row.Up, row.Down, row.Enable, row.DisabledBy, row.ManagerRevision})
	}
	c.JSON(200, gin.H{"observedAt": time.Now().Unix(), "inbounds": out})
}

func (a *AgentAPI) upsert(c *gin.Context) {
	port, ok := apiPort(c)
	if !ok {
		return
	}
	var payload model.Inbound
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(400, gin.H{"error": "invalid inbound"})
		return
	}
	if payload.ManagerAccountID <= 0 || payload.Port != port || len(payload.ManagerRevision) != 64 || payload.Protocol == "" || !json.Valid([]byte(payload.Settings)) || !json.Valid([]byte(payload.StreamSettings)) {
		c.JSON(400, gin.H{"error": "invalid inbound configuration"})
		return
	}
	agentMutationMu.Lock()
	defer agentMutationMu.Unlock()
	if err := a.inbounds.ApplyPanelCertificates(&payload); err != nil {
		apiError(c, err)
		return
	}
	var existing model.Inbound
	err := database.GetDB().Where("manager_account_id = ?", payload.ManagerAccountID).First(&existing).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		var conflict int64
		if err := database.GetDB().Model(&model.Inbound{}).Where("port = ?", port).Count(&conflict).Error; err != nil {
			apiError(c, err)
			return
		}
		if conflict > 0 {
			c.JSON(409, gin.H{"error": "port already used"})
			return
		}
		var owner model.User
		if err := database.GetDB().First(&owner).Error; err != nil {
			apiError(c, err)
			return
		}
		payload.Id = 0
		payload.UserId = owner.Id
		payload.Tag = "inbound-" + strconv.Itoa(port)
		err = a.inbounds.AddInbound(&payload)
	} else if err == nil {
		payload.Id = existing.Id
		payload.UserId = existing.UserId
		err = a.inbounds.UpdateInbound(&payload)
	} else {
		apiError(c, err)
		return
	}
	if err != nil {
		apiError(c, err)
		return
	}
	if err := service.ApplyManagedXray(); err != nil {
		apiError(c, err)
		return
	}
	c.JSON(200, gin.H{"applied": true, "port": port})
}

func (a *AgentAPI) remove(c *gin.Context) {
	port, ok := apiPort(c)
	if !ok {
		return
	}
	var body struct {
		AccountID int `json:"accountId"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.AccountID <= 0 {
		c.JSON(400, gin.H{"error": "accountId required"})
		return
	}
	agentMutationMu.Lock()
	defer agentMutationMu.Unlock()
	var in model.Inbound
	err := database.GetDB().Where("manager_account_id = ? AND port = ?", body.AccountID, port).First(&in).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if err := service.ApplyManagedXray(); err != nil {
			apiError(c, err)
			return
		}
		c.JSON(200, gin.H{"applied": true})
		return
	}
	if err != nil {
		apiError(c, err)
		return
	}
	if err := a.inbounds.DelInbound(in.Id); err != nil {
		apiError(c, err)
		return
	}
	if err := service.ApplyManagedXray(); err != nil {
		apiError(c, err)
		return
	}
	c.JSON(200, gin.H{"applied": true})
}

func (a *AgentAPI) disable(c *gin.Context) {
	port, ok := apiPort(c)
	if !ok {
		return
	}
	var body struct {
		AccountID       int    `json:"accountId"`
		Reason          string `json:"reason"`
		ManagerRevision string `json:"managerRevision"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.AccountID <= 0 || len(body.ManagerRevision) != 64 || (body.Reason != "limit" && body.Reason != "expired") {
		c.JSON(400, gin.H{"error": "invalid disable request"})
		return
	}
	agentMutationMu.Lock()
	defer agentMutationMu.Unlock()
	result := database.GetDB().Model(&model.Inbound{}).Where("manager_account_id = ? AND port = ?", body.AccountID, port).Updates(map[string]interface{}{"enable": false, "disabled_by": body.Reason, "manager_revision": body.ManagerRevision})
	if result.Error != nil {
		apiError(c, result.Error)
		return
	}
	if err := service.ApplyManagedXray(); err != nil {
		apiError(c, err)
		return
	}
	c.JSON(200, gin.H{"applied": true})
}

func (a *AgentAPI) reset(c *gin.Context) {
	if !a.ready(c) {
		return
	}
	port, ok := apiPort(c)
	if !ok {
		return
	}
	var body struct {
		AccountID   int    `json:"accountId"`
		Period      int    `json:"period"`
		OperationID string `json:"operationId"`
	}
	if err := c.ShouldBindJSON(&body); err != nil || body.AccountID <= 0 || (body.Period != 0 && body.Period < 202001) || len(body.OperationID) < 12 {
		c.JSON(400, gin.H{"error": "invalid reset request"})
		return
	}
	result, err := service.ResetAgentTraffic(body.AccountID, port, body.Period, body.OperationID)
	if err != nil {
		apiError(c, err)
		return
	}
	c.Data(200, "application/json", result)
}
