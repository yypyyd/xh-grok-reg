package handlers

import (
	"net/http"

	"xh-grok-reg/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm/clause"
)

// 内部保留 key（如 JWT 密钥），不允许通过设置接口读取或修改
var reservedSettingKeys = map[string]bool{
	"jwt_secret": true,
}

func (h *Handler) SettingsGet(c *gin.Context) {
	var items []models.Setting
	if err := h.DB.Find(&items).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	out := map[string]string{}
	for _, s := range items {
		if reservedSettingKeys[s.Key] {
			continue
		}
		out[s.Key] = s.Value
	}
	c.JSON(http.StatusOK, out)
}

func (h *Handler) SettingsSave(c *gin.Context) {
	var in map[string]string
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	for k, v := range in {
		if reservedSettingKeys[k] {
			continue
		}
		s := models.Setting{Key: k, Value: v}
		if err := h.DB.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "key"}},
			DoUpdates: clause.AssignmentColumns([]string{"value", "updated_at"}),
		}).Create(&s).Error; err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}
