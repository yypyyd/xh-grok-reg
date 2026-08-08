package db

import (
	"xh-grok-reg/internal/emailalias"
	"xh-grok-reg/internal/models"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func Init(path string) (*gorm.DB, error) {
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
	if err != nil {
		return nil, err
	}
	if err := db.AutoMigrate(&models.GrokRegistration{}, &models.Mailbox{}, &models.Setting{}, &models.Admin{}); err != nil {
		return nil, err
	}
	reclaimOrphanGrokRegistering(db)
	backfillGrokMailboxIDs(db)
	return db, nil
}

func reclaimOrphanGrokRegistering(db *gorm.DB) {
	db.Model(&models.GrokRegistration{}).Where("status IN ?", []string{"registering", "waiting_code"}).
		Updates(map[string]any{"status": "register_failed", "note": "程序重启中断，可重新注册"})
}

// backfillGrokMailboxIDs associates legacy Grok rows with their mailbox.
func backfillGrokMailboxIDs(db *gorm.DB) {
	var regs []models.GrokRegistration
	if err := db.Where("mailbox_id IS NULL OR mailbox_id = 0").Find(&regs).Error; err != nil {
		return
	}
	for _, reg := range regs {
		baseEmail := emailalias.Base(reg.Email)
		var mb models.Mailbox
		if err := db.Where("email = ?", baseEmail).First(&mb).Error; err == nil {
			db.Model(&models.GrokRegistration{}).Where("id = ?", reg.ID).Update("mailbox_id", mb.ID)
		}
	}
}
