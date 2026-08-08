package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
	"time"

	"xh-grok-reg/internal/livecheck"
	"xh-grok-reg/internal/models"

	"github.com/gin-gonic/gin"
)

type liveCheckReq struct {
	IDs []uint `json:"ids"`
}

type liveRunner struct {
	mu                                sync.Mutex
	running                           bool
	total, done, alive, dead, unknown int
	message                           string
}

func newLiveRunners() map[string]*liveRunner { return map[string]*liveRunner{"grok": {}} }

func (r *liveRunner) tryStart() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.running {
		return false
	}
	r.running = true
	r.total, r.done, r.alive, r.dead, r.unknown = 0, 0, 0, 0, 0
	r.message = "测活中..."
	return true
}
func (r *liveRunner) setTotal(n int) { r.mu.Lock(); r.total = n; r.mu.Unlock() }
func (r *liveRunner) tally(status string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.done++
	switch status {
	case livecheck.StatusAlive:
		r.alive++
	case livecheck.StatusDead:
		r.dead++
	default:
		r.unknown++
	}
}
func (r *liveRunner) finish(msg string) {
	r.mu.Lock()
	r.running = false
	if msg != "" {
		r.message = msg
	} else {
		r.message = "测活完成"
	}
	r.mu.Unlock()
}
func (r *liveRunner) snapshot() gin.H {
	r.mu.Lock()
	defer r.mu.Unlock()
	return gin.H{"running": r.running, "total": r.total, "done": r.done, "alive": r.alive, "dead": r.dead, "unknown": r.unknown, "message": r.message}
}

func (h *Handler) liveRunnerFor(platform string) *liveRunner {
	if r, ok := h.Live[platform]; ok {
		return r
	}
	return &liveRunner{}
}

func (h *Handler) GrokLiveCheckStart(c *gin.Context) {
	var in liveCheckReq
	_ = c.ShouldBindJSON(&in)
	runner := h.liveRunnerFor("grok")
	if !runner.tryStart() {
		c.JSON(http.StatusConflict, gin.H{"error": "已有测活任务进行中"})
		return
	}
	items, err := h.loadGrokItems(in.IDs)
	if err != nil {
		runner.finish("加载账号失败")
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if len(items) == 0 {
		runner.finish("没有可测活的 Grok 账号")
		c.JSON(http.StatusBadRequest, gin.H{"error": "没有可测活的 Grok 账号（需已注册且有会话数据）"})
		return
	}
	runner.setTotal(len(items))
	go func() {
		defer runner.finish("")
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		livecheck.CheckGrok(ctx, items, func(chunk map[uint]livecheck.GrokResult) {
			for id, res := range chunk {
				h.applyGrokAlive(id, res)
				runner.tally(res.Status)
			}
		})
	}()
	c.JSON(http.StatusOK, gin.H{"ok": true, "total": len(items)})
}

func (h *Handler) GrokLiveCheckStatus(c *gin.Context) {
	c.JSON(http.StatusOK, h.liveRunnerFor("grok").snapshot())
}

func (h *Handler) GrokLiveCheckOne(c *gin.Context) {
	id64, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	item, ok := h.loadGrokItem(uint(id64))
	if !ok {
		c.JSON(http.StatusBadRequest, gin.H{"error": "该 Grok 账号没有可测活的会话数据"})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	res := livecheck.CheckGrok(ctx, []livecheck.GrokItem{item}, nil)[item.ID]
	if res.Status == "" {
		res.Status = livecheck.StatusUnknown
	}
	now := h.applyGrokAlive(item.ID, res)
	c.JSON(http.StatusOK, gin.H{"id": item.ID, "alive": res.Status, "console_quota": res.Quota, "checked_at": now})
}

func (h *Handler) applyGrokAlive(id uint, res livecheck.GrokResult) time.Time {
	now := time.Now()
	fields := map[string]any{"alive": res.Status, "alive_checked_at": now}
	if res.Quota != "" {
		fields["console_quota"] = res.Quota
	}
	h.DB.Model(&models.GrokRegistration{}).Where("id = ?", id).Updates(fields)
	return now
}

func (h *Handler) loadGrokItems(ids []uint) ([]livecheck.GrokItem, error) {
	var regs []models.GrokRegistration
	q := h.DB.Select("id", "auth_data").Where("auth_data <> ''")
	if len(ids) > 0 {
		q = q.Where("id IN ?", ids)
	} else {
		q = q.Where("status = ?", "registered")
	}
	if err := q.Find(&regs).Error; err != nil {
		return nil, err
	}
	items := make([]livecheck.GrokItem, 0, len(regs))
	for _, r := range regs {
		items = append(items, grokLiveItem(r))
	}
	return items, nil
}

func (h *Handler) loadGrokItem(id uint) (livecheck.GrokItem, bool) {
	var r models.GrokRegistration
	if err := h.DB.Select("id", "auth_data").First(&r, id).Error; err != nil || r.AuthData == "" {
		return livecheck.GrokItem{}, false
	}
	return grokLiveItem(r), true
}

func grokLiveItem(r models.GrokRegistration) livecheck.GrokItem {
	refresh, endpoint, clientID := grokRefreshCreds(r.AuthData)
	return livecheck.GrokItem{ID: r.ID, SSO: grokSSOToken(r.AuthData), RefreshToken: refresh, TokenEndpoint: endpoint, ClientID: clientID}
}

func grokRefreshCreds(authData string) (refresh, endpoint, clientID string) {
	var m map[string]any
	if json.Unmarshal([]byte(authData), &m) != nil {
		return "", "", ""
	}
	cpa, _ := m["cpa_xai"].(map[string]any)
	if cpa == nil {
		return "", "", ""
	}
	refresh, _ = cpa["refresh_token"].(string)
	endpoint, _ = cpa["token_endpoint"].(string)
	clientID, _ = cpa["client_id"].(string)
	return
}
