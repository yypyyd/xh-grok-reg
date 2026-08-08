package handlers

import (
	"net/http"

	"xh-grok-reg/internal/auth"
	"xh-grok-reg/internal/browserboot"
	"xh-grok-reg/internal/grokproducer"
	"xh-grok-reg/internal/mailfetch"
	"xh-grok-reg/internal/mailverify"
	"xh-grok-reg/internal/models"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// Handler wires the Grok API to shared authentication, mailbox and storage services.
type Handler struct {
	DB              *gorm.DB
	Mail            *mailfetch.Client
	Auth            *auth.Service
	GrokProducer    *grokproducer.Producer
	Browser         *browserboot.Manager
	MailboxVerifier *mailverify.Service
	Live            map[string]*liveRunner
}

func New(db *gorm.DB, authSvc *auth.Service, browser *browserboot.Manager) (*Handler, error) {
	mail := mailfetch.New()
	verifier := mailverify.New(db, mail, mailverify.DefaultConcurrency)
	if err := verifier.Start(); err != nil {
		return nil, err
	}
	return &Handler{
		DB: db, Mail: mail, Auth: authSvc,
		GrokProducer: grokproducer.New(db, mail),
		Browser:      browser, MailboxVerifier: verifier, Live: newLiveRunners(),
	}, nil
}

// Stats returns the dashboard summary for the Grok account pool and mailbox pool.
func (h *Handler) Stats(c *gin.Context) {
	count := func(where ...any) int64 {
		var n int64
		q := h.DB.Model(&models.GrokRegistration{})
		if len(where) > 0 {
			q = q.Where(where[0], where[1:]...)
		}
		q.Count(&n)
		return n
	}

	total := count()
	pending := count("status = ?", "pending")
	registering := count("status IN ?", []string{"registering", "waiting_code"})
	registered := count("status = ?", "registered")
	failed := count("status = ?", "register_failed")
	shipped := count("shipped = ?", true)

	var mailboxes, mailboxVerified int64
	h.DB.Model(&models.Mailbox{}).Count(&mailboxes)
	h.DB.Model(&models.Mailbox{}).Where("status = ?", "verified").Count(&mailboxVerified)

	prog := h.GrokProducer.Snapshot()
	c.JSON(http.StatusOK, gin.H{
		"total": total, "pending": pending, "registering": registering,
		"registered": registered, "register_failed": failed,
		"shipped": shipped, "unshipped": registered - shipped,
		"mailboxes": mailboxes, "mailbox_verified": mailboxVerified,
		"running": prog.Running, "produce_target": prog.Target,
		"produce_pending": prog.Pending, "produce_running": prog.RunningNum,
		"produce_registered": prog.Registered, "produce_failed": prog.Failed,
		"produce_message": prog.Message,
	})
}
