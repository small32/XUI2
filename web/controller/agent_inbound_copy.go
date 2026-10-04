package controller

import (
	"net/http"
	"xui/web/service"

	"github.com/gin-gonic/gin"
)

func (a *AgentAPI) listAllInbounds(c *gin.Context) {
	if !a.ready(c) {
		return
	}
	rows, err := a.inbounds.GetAllInbounds()
	if err != nil {
		apiError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"inbounds": rows})
}

func (a *AgentAPI) copyInbound(c *gin.Context) {
	port, ok := apiPort(c)
	if !ok {
		return
	}
	var request service.InboundCopyPayload
	if err := c.ShouldBindJSON(&request); err != nil || request.Inbound.Port != port {
		c.JSON(400, gin.H{"error": "invalid inbound configuration"})
		return
	}
	agentMutationMu.Lock()
	defer agentMutationMu.Unlock()
	if err := service.ImportNodeInbound(request.Inbound, request.Overwrite); err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}
	if err := service.ApplyManagedXray(); err != nil {
		apiError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"applied": true})
}
