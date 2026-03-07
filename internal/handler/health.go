package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/trae/midjourney-api/pkg/response"
)

type HealthHandler struct{}

func NewHealthHandler() *HealthHandler {
	return &HealthHandler{}
}

// LivenessCheck is the liveness probe endpoint
// @Summary Liveness check
// @Description Check if the service is alive (simple ping)
// @Tags System
// @Produce json
// @Success 200 {object} response.Response "Service is alive"
// @Router /live [get]
func (h *HealthHandler) LivenessCheck(c *gin.Context) {
	response.Success(c, gin.H{"status": "alive"})
}
