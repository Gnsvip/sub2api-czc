package admin

import (
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func excelAuthorizationAccountID(c *gin.Context) (int64, bool) {
	id, err := strconv.ParseInt(c.Param("id"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid account ID")
		return 0, false
	}
	return id, true
}
func (h *OpenAIOAuthHandler) BeginExcelAuthorization(c *gin.Context) {
	id, ok := excelAuthorizationAccountID(c)
	if !ok {
		return
	}
	result, err := h.openaiOAuthService.BeginExcelAuthorization(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, result)
}
func (h *OpenAIOAuthHandler) CompleteExcelAuthorization(c *gin.Context) {
	id, ok := excelAuthorizationAccountID(c)
	if !ok {
		return
	}
	var req OpenAIExchangeCodeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		response.BadRequest(c, "Invalid authorization callback")
		return
	}
	state, err := h.openaiOAuthService.CompleteExcelAuthorization(c.Request.Context(), id, &service.OpenAIExchangeCodeInput{SessionID: req.SessionID, Code: req.Code, State: req.State})
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, state)
}
func (h *OpenAIOAuthHandler) ExcelAuthorizationState(c *gin.Context) {
	id, ok := excelAuthorizationAccountID(c)
	if !ok {
		return
	}
	state, err := h.openaiOAuthService.ExcelAuthorizationState(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, state)
}
