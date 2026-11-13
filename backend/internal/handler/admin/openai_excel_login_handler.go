package admin

import (
	"net/http"
	"strconv"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
)

func (h *OpenAIOAuthHandler) ExcelLoginStatus(c *gin.Context) {
	id, ok := excelAuthorizationAccountID(c)
	if !ok {
		return
	}
	state, err := h.openaiOAuthService.ExcelLoginStatus(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, state)
}
func (h *OpenAIOAuthHandler) SaveExcelLogin(c *gin.Context) {
	id, ok := excelAuthorizationAccountID(c)
	if !ok {
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 16<<10)
	var input service.ExcelLoginInput
	if c.ShouldBindJSON(&input) != nil {
		response.BadRequest(c, "Invalid login configuration")
		return
	}
	state, err := h.openaiOAuthService.SaveExcelLogin(c.Request.Context(), id, input)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, state)
}
func (h *OpenAIOAuthHandler) QueueExcelLogin(c *gin.Context) {
	id, ok := excelAuthorizationAccountID(c)
	if !ok {
		return
	}
	state, err := h.openaiOAuthService.QueueExcelLogin(c.Request.Context(), id)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, state)
}

// Separate worker capability: no administrator JWT, query token or ordinary API
// key can claim encrypted password material. Disable caching on all responses.
func (h *OpenAIOAuthHandler) ExcelLoginWorkerAuth(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if h == nil || h.openaiOAuthService == nil || !h.openaiOAuthService.ExcelLoginWorkerAuthenticated(c.GetHeader("X-OpenAI-Reauth-Worker-Token")) {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"code": 401, "message": "Invalid worker authentication"})
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20)
	c.Next()
}

type excelLoginWorkerInput struct {
	WorkerID    string         `json:"worker_id"`
	Stage       string         `json:"stage"`
	Reason      string         `json:"reason"`
	Credentials map[string]any `json:"credentials"`
}

func (h *OpenAIOAuthHandler) ExcelLoginClaim(c *gin.Context) {
	var input excelLoginWorkerInput
	if c.ShouldBindJSON(&input) != nil {
		response.BadRequest(c, "Invalid worker request")
		return
	}
	claim, err := h.openaiOAuthService.ClaimExcelLogin(c.Request.Context(), input.WorkerID)
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, claim)
}
func (h *OpenAIOAuthHandler) ExcelLoginWorkerUpdate(c *gin.Context) {
	id, err := strconv.ParseInt(c.Param("task"), 10, 64)
	if err != nil || id <= 0 {
		response.BadRequest(c, "Invalid task ID")
		return
	}
	var input excelLoginWorkerInput
	if c.ShouldBindJSON(&input) != nil {
		response.BadRequest(c, "Invalid worker request")
		return
	}
	switch c.Param("action") {
	case "credentials":
		result, e := h.openaiOAuthService.CompleteExcelLogin(c.Request.Context(), id, input.WorkerID, input.Credentials)
		if e != nil {
			response.ErrorFrom(c, e)
			return
		}
		response.Success(c, result)
		return
	case "progress":
		err = h.openaiOAuthService.ProgressExcelLogin(c.Request.Context(), id, input.WorkerID, input.Stage)
	case "fail":
		err = h.openaiOAuthService.FailExcelLogin(c.Request.Context(), id, input.WorkerID, input.Reason)
	default:
		response.NotFound(c, "Unknown worker operation")
		return
	}
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, gin.H{"ok": true})
}

func (h *OpenAIOAuthHandler) GetBPSDefaults(c *gin.Context) {
	defaults, err := h.openaiOAuthService.ExcelBPSDefaults(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, defaults)
}
func (h *OpenAIOAuthHandler) SaveBPSDefaults(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 32<<10)
	var input service.ExcelBPSDefaults
	if c.ShouldBindJSON(&input) != nil {
		response.BadRequest(c, "Invalid BPS defaults")
		return
	}
	if err := h.openaiOAuthService.SaveExcelBPSDefaults(c.Request.Context(), input); err != nil {
		response.ErrorFrom(c, err)
		return
	}
	response.Success(c, input)
}
