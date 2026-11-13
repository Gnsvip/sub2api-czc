package admin

import (
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/gin-gonic/gin"
)

func (h *AccountHandler) SetOpenAIAstraProbeInterval(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid account ID")
		return
	}
	var req struct {
		Minutes *int `json:"interval_minutes" binding:"required"`
	}
	if err = c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid request: "+err.Error())
		return
	}
	a, err := h.adminService.SetOpenAIAstraProbeInterval(c.Request.Context(), id, *req.Minutes)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	h.accountTestService.RefreshOpenAIAstraProbeInterval(a)
	response.Success(c, h.buildAccountResponseWithRuntime(c.Request.Context(), a))
}

func (h *AccountHandler) RunOpenAIAstraProbe(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid account ID")
		return
	}
	// Use the normal account lookup for 404s and the existing admin route auth.
	if _, err = h.adminService.GetAccount(c.Request.Context(), id); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	if err = h.accountTestService.RunOpenAIAstraProbe(c.Request.Context(), id); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	a, err := h.adminService.GetAccount(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, h.buildAccountResponseWithRuntime(c.Request.Context(), a))
}
