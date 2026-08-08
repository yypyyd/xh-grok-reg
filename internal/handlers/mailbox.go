package handlers

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"xh-grok-reg/internal/emailalias"
	"xh-grok-reg/internal/mailfetch"
	"xh-grok-reg/internal/models"

	"github.com/gin-gonic/gin"
)

type mailboxInput struct {
	Email        string `json:"email" binding:"required"`
	Password     string `json:"password"`
	Provider     string `json:"provider"`
	ClientID     string `json:"client_id"`
	RefreshToken string `json:"refresh_token"`
	Status       string `json:"status"`
	Note         string `json:"note"`
}

var mailboxStatuses = map[string]bool{
	"unverified":    true,
	"verifying":     true,
	"verify_failed": true,
	"verified":      true,
}

func validMailboxStatus(s string) bool {
	return s == "" || mailboxStatuses[s]
}

func (h *Handler) MailboxList(c *gin.Context) {
	var items []models.Mailbox
	q := h.DB.Order("id desc")
	if s := c.Query("status"); s != "" {
		q = q.Where("status = ?", s)
	}
	if kw := c.Query("q"); kw != "" {
		like := "%" + kw + "%"
		q = q.Where("email LIKE ? OR provider LIKE ? OR note LIKE ?", like, like, like)
	}
	page, _ := strconv.Atoi(c.DefaultQuery("page", "1"))
	size, _ := strconv.Atoi(c.DefaultQuery("size", "20"))
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 100 {
		size = 20
	}
	var total int64
	q.Model(&models.Mailbox{}).Count(&total)
	if err := q.Offset((page - 1) * size).Limit(size).Find(&items).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	registerLimit := 1 + h.fissionCount()
	for i := range items {
		items[i].RegisterCount = h.mailboxRegisterCount(items[i])
		items[i].RegisterLimit = registerLimit
	}
	c.JSON(http.StatusOK, gin.H{"data": items, "total": total, "page": page, "size": size})
}

func (h *Handler) fissionCount() int {
	var s models.Setting
	if err := h.DB.Where("key = ?", "fission_count").First(&s).Error; err != nil {
		return 5
	}
	n, err := strconv.Atoi(strings.TrimSpace(s.Value))
	if err != nil || n < 0 {
		return 5
	}
	return n
}

func (h *Handler) mailboxRegisterCount(m models.Mailbox) int {
	var n int64
	q := h.DB.Model(&models.GrokRegistration{}).Where("mailbox_id = ? OR email = ?", m.ID, m.Email)
	if pattern := emailalias.LikePattern(m.Email); pattern != "" {
		q = q.Or("email LIKE ? ESCAPE '\\'", pattern)
	}
	q.Count(&n)
	return int(n)
}

func (h *Handler) MailboxCreate(c *gin.Context) {
	var in mailboxInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if !validMailboxStatus(in.Status) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid status"})
		return
	}
	m := models.Mailbox{Email: in.Email, Password: in.Password, Provider: in.Provider, ClientID: in.ClientID, RefreshToken: in.RefreshToken, Status: in.Status, Note: in.Note}
	if m.Status == "" {
		m.Status = "unverified"
	}
	if err := h.DB.Create(&m).Error; err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, m)
}

type mailboxImportItem struct {
	Email        string `json:"email"`
	Password     string `json:"password"`
	ClientID     string `json:"client_id"`
	RefreshToken string `json:"refresh_token"`
}

// MailboxImport 批量导入邮箱，重复 email 自动跳过。
func (h *Handler) MailboxImport(c *gin.Context) {
	var in struct {
		Items []mailboxImportItem `json:"items" binding:"required"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	added, skipped := 0, 0
	ids := make([]uint, 0, len(in.Items))
	seen := map[string]bool{}
	for _, it := range in.Items {
		email := strings.TrimSpace(it.Email)
		if email == "" || !strings.Contains(email, "@") || seen[email] {
			skipped++
			continue
		}
		seen[email] = true
		var count int64
		h.DB.Model(&models.Mailbox{}).Where("email = ?", email).Count(&count)
		if count > 0 {
			skipped++
			continue
		}
		m := models.Mailbox{
			Email:        email,
			Password:     strings.TrimSpace(it.Password),
			ClientID:     strings.TrimSpace(it.ClientID),
			RefreshToken: strings.TrimSpace(it.RefreshToken),
			Status:       "unverified",
		}
		if err := h.DB.Create(&m).Error; err != nil {
			skipped++
			continue
		}
		added++
		ids = append(ids, m.ID)
	}
	if len(ids) > 0 {
		h.MailboxVerifier.Wake()
	}
	c.JSON(http.StatusOK, gin.H{"added": added, "skipped": skipped, "queued": len(ids)})
}

// MailboxVerify 把单个邮箱提交给持久化认证队列。
func (h *Handler) MailboxVerify(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效邮箱 ID"})
		return
	}
	queued, err := h.MailboxVerifier.Reauthenticate([]uint{uint(id)})
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if queued == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "邮箱不存在"})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"id": uint(id), "status": "queued"})
}

// MailboxReauthenticate queues selected, failed, or every mailbox for fresh authentication.
func (h *Handler) MailboxReauthenticate(c *gin.Context) {
	var in struct {
		IDs    []uint `json:"ids"`
		Failed bool   `json:"failed"`
		All    bool   `json:"all"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	modes := 0
	if len(in.IDs) > 0 {
		modes++
	}
	if in.Failed {
		modes++
	}
	if in.All {
		modes++
	}
	if modes != 1 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请选择一种重新认证范围"})
		return
	}
	var queued int64
	var err error
	if in.Failed {
		queued, err = h.MailboxVerifier.ReauthenticateFailed()
	} else if in.All {
		queued, err = h.MailboxVerifier.ReauthenticateAll()
	} else {
		queued, err = h.MailboxVerifier.Reauthenticate(in.IDs)
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"queued": queued})
}

func (h *Handler) MailboxUpdate(c *gin.Context) {
	var m models.Mailbox
	if err := h.DB.First(&m, c.Param("id")).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	var in mailboxInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if !validMailboxStatus(in.Status) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid status"})
		return
	}
	m.Email = in.Email
	m.Password = in.Password
	m.Provider = in.Provider
	m.ClientID = in.ClientID
	m.RefreshToken = in.RefreshToken
	if in.Status != "" {
		m.Status = in.Status
	}
	m.Note = in.Note
	if err := h.DB.Save(&m).Error; err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, m)
}

func (h *Handler) MailboxDelete(c *gin.Context) {
	if err := h.DB.Delete(&models.Mailbox{}, c.Param("id")).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *Handler) MailboxDeleteAll(c *gin.Context) {
	r := h.DB.Where("1 = 1").Delete(&models.Mailbox{})
	if r.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": r.Error.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": r.RowsAffected})
}

// MailboxMessages 取件：拉某个邮箱收件箱最新邮件，含完整 HTML 正文，供网页弹窗轮询展示。
func (h *Handler) MailboxMessages(c *gin.Context) {
	var m models.Mailbox
	if err := h.DB.First(&m, c.Param("id")).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "邮箱不存在"})
		return
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "20"))
	msgs, err := h.Mail.ListMessages(c.Request.Context(), mailfetch.Account{
		Email:        m.Email,
		ClientID:     m.ClientID,
		RefreshToken: m.RefreshToken,
	}, limit)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, mailfetch.ErrMissingCreds) || errors.Is(err, mailfetch.ErrAuthFailed) {
			status = http.StatusBadRequest
		}
		c.JSON(status, gin.H{"error": err.Error(), "email": m.Email})
		return
	}
	c.JSON(http.StatusOK, gin.H{"email": m.Email, "items": msgs})
}

// MailboxMessage 按消息 ID 拉取单封邮件的完整正文，供点击后按需加载。
func (h *Handler) MailboxMessage(c *gin.Context) {
	var m models.Mailbox
	if err := h.DB.First(&m, c.Param("id")).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "邮箱不存在"})
		return
	}
	msg, err := h.Mail.GetMessage(c.Request.Context(), mailfetch.Account{
		Email:        m.Email,
		ClientID:     m.ClientID,
		RefreshToken: m.RefreshToken,
	}, c.Query("mid"))
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, mailfetch.ErrMissingCreds) || errors.Is(err, mailfetch.ErrAuthFailed) {
			status = http.StatusBadRequest
		}
		c.JSON(status, gin.H{"error": err.Error(), "email": m.Email})
		return
	}
	c.JSON(http.StatusOK, msg)
}
