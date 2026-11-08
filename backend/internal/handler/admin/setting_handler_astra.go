package admin

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func (h *SettingHandler) GetAstraAutoGroupSettings(c *gin.Context) {
	settings, err := h.settingService.GetAstraAutoGroupSettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, settings)
}

func (h *SettingHandler) UpdateAstraAutoGroupSettings(c *gin.Context) {
	var req struct {
		Enabled         *bool `json:"enabled" binding:"required"`
		HealthyGroupID  int64 `json:"healthy_group_id"`
		DegradedGroupID int64 `json:"degraded_group_id"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	settings := service.AstraAutoGroupSettings{Enabled: *req.Enabled, HealthyGroupID: req.HealthyGroupID, DegradedGroupID: req.DegradedGroupID}
	if err := h.settingService.UpdateAstraAutoGroupSettings(c.Request.Context(), settings); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, settings)
}
