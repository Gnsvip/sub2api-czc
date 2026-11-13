package routes

import (
	"github.com/Wei-Shaw/sub2api/internal/handler"
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/gin-gonic/gin"
)

func RegisterExcelLoginWorkerRoutes(v1 *gin.RouterGroup, h *handler.Handlers) {
	if h == nil || h.Admin == nil || h.Admin.OpenAIOAuth == nil {
		return
	}
	worker := v1.Group("/internal/openai-reauth")
	worker.Use(h.Admin.OpenAIOAuth.ExcelLoginWorkerAuth)
	worker.POST("/claim", h.Admin.OpenAIOAuth.ExcelLoginClaim)
	worker.POST("/runtime-settings", func(c *gin.Context) { response.Success(c, gin.H{"worker_concurrency": 3}) })
	// Upstream worker also polls these independent queues. This adapter offers
	// only Excel grants; TOTP rotation and session exports are never enqueued.
	worker.POST("/totp-claim", func(c *gin.Context) { response.Success(c, nil) })
	worker.POST("/session-export/claim", func(c *gin.Context) { response.Success(c, nil) })
	worker.POST("/:task/:action", h.Admin.OpenAIOAuth.ExcelLoginWorkerUpdate)
}
