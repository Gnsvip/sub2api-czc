package handler

import (
	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/service"

	"github.com/gin-gonic/gin"
)

// GetPublicGuide returns only the currently published guide. Revision content
// remains available exclusively through the authenticated admin API.
func (h *SettingHandler) GetPublicGuide(c *gin.Context) {
	settings, err := h.settingService.GetGuideSettings(c.Request.Context())
	if err != nil {
		response.ErrorFrom(c, err)
		return
	}
	// Saved custom content remains editable by admins, but it must not shadow
	// the bundled release guide or be exposed as active content in bundled mode.
	content, chapters := "", []service.GuideChapter{}
	custom := settings.DisplayMode == service.GuideDisplayModeCustom && settings.HasCustomContent
	if custom {
		content, chapters = settings.Content, settings.Chapters
	}

	response.Success(c, struct {
		Content          string                 `json:"content"`
		DisplayMode      string                 `json:"display_mode"`
		Chapters         []service.GuideChapter `json:"chapters"`
		Version          int                    `json:"version"`
		UpdatedAt        string                 `json:"updated_at"`
		HasCustomContent bool                   `json:"has_custom_content"`
	}{
		Content:          content,
		DisplayMode:      settings.DisplayMode,
		Chapters:         chapters,
		Version:          settings.Version,
		UpdatedAt:        settings.UpdatedAt,
		HasCustomContent: custom,
	})
}
