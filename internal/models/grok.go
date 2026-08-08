package models

import "time"

// GrokRegistration stores one Grok account and its registration/session state.
type GrokRegistration struct {
	ID        uint   `gorm:"primaryKey" json:"id"`
	Email     string `gorm:"size:255;not null;uniqueIndex" json:"email"`
	MailboxID uint   `gorm:"index" json:"mailbox_id"`
	Password  string `gorm:"size:255" json:"-"`
	Status    string `gorm:"size:32;default:pending" json:"status"`
	Shipped   bool   `gorm:"default:false" json:"shipped"`
	AuthData  string `gorm:"type:text" json:"auth_data,omitempty"`
	Log       string `gorm:"type:text" json:"log,omitempty"`
	Shot      []byte `gorm:"type:blob" json:"-"`
	Note      string `gorm:"type:text" json:"note"`

	// 手动测活结果：alive / dead / unknown（空=未测），unknown 不判死。
	Alive          string     `gorm:"size:16;default:''" json:"alive"`
	AliveCheckedAt *time.Time `json:"alive_checked_at,omitempty"`
	// 测活时从 Grok Console /v1/usage 读到的额度摘要，如「chat 20/20 · image 5/5」。
	ConsoleQuota string `gorm:"size:255;default:''" json:"console_quota"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
