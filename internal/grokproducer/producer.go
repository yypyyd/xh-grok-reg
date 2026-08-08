package grokproducer

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"xh-grok-reg/internal/grokreg"
	"xh-grok-reg/internal/mailfetch"
	"xh-grok-reg/internal/models"
	"xh-grok-reg/internal/proxyutil"

	"gorm.io/gorm"
)

const (
	codeWaitTimeout  = 10 * time.Minute
	codePollTimeout  = 4 * time.Minute
	codePollInterval = 5 * time.Second
	maxLogBytes      = 64 * 1024

	// defaultMaxConcurrency 未配置任何并发键时的默认值：逐个开工。批量注册时多个
	// 有头浏览器 + Turnstile 令牌池同时抢 CPU 会互相超时，串行最稳。可用设置页
	// 「最大并发数」(max_concurrency) 或专用键 grok_max_concurrency 调大。
	defaultMaxConcurrency = 1
)

var (
	digitCodeRe = regexp.MustCompile(`\b(\d{6})\b`)
	xaiCodeRe   = regexp.MustCompile(`(?i)\b([a-z0-9]{3})-([a-z0-9]{3})\b`)
)

type Producer struct {
	db   *gorm.DB
	mail *mailfetch.Client

	mu      sync.Mutex
	waiters map[uint]chan string
	cancel  map[uint]context.CancelFunc
	active  int // 当前真正在跑（已获得并发槽位）的任务数
	pxIdx   int
	target  int
}

func New(db *gorm.DB, mail *mailfetch.Client) *Producer {
	return &Producer{
		db:      db,
		mail:    mail,
		waiters: map[uint]chan string{},
		cancel:  map[uint]context.CancelFunc{},
	}
}

func (p *Producer) Start(email, note string) (*models.GrokRegistration, error) {
	email = strings.TrimSpace(email)
	if email == "" {
		return nil, fmt.Errorf("邮箱不能为空")
	}
	var mb models.Mailbox
	mailboxID := uint(0)
	if err := p.db.Where("email = ? AND status = ?", email, "verified").First(&mb).Error; err == nil {
		mailboxID = mb.ID
		if strings.TrimSpace(note) == "" {
			note = fmt.Sprintf("来源: 邮箱管理 #%d，自动读取验证码", mb.ID)
		}
	}

	var existing models.GrokRegistration
	if err := p.db.Where("email = ?", email).First(&existing).Error; err == nil {
		if existing.Status == "registering" || existing.Status == "waiting_code" {
			return nil, fmt.Errorf("该邮箱的 Grok 注册正在进行中")
		}
		existing.Status = "registering"
		existing.Note = note
		existing.MailboxID = mailboxID
		existing.Password = grokreg.GenPassword(16)
		existing.AuthData = ""
		existing.Shot = nil
		existing.Shipped = false
		if err := p.db.Save(&existing).Error; err != nil {
			return nil, err
		}
		go p.run(existing.ID)
		return &existing, nil
	}

	reg := models.GrokRegistration{Email: email, MailboxID: mailboxID, Password: grokreg.GenPassword(16), Status: "registering", Note: note}
	if err := p.db.Create(&reg).Error; err != nil {
		return nil, err
	}
	go p.run(reg.ID)
	return &reg, nil
}

func (p *Producer) StartFromAccounts(count int) ([]models.GrokRegistration, error) {
	if count < 1 {
		return nil, fmt.Errorf("数量必须 >= 1")
	}
	p.mu.Lock()
	p.target = count
	p.mu.Unlock()
	var mailboxes []models.Mailbox
	if err := p.db.
		Where("status = ?", "verified").
		Where("email NOT IN (?)",
			p.db.Model(&models.GrokRegistration{}).Select("email")).
		Order("id asc").
		Limit(count).
		Find(&mailboxes).Error; err != nil {
		return nil, err
	}
	started := make([]models.GrokRegistration, 0, len(mailboxes))
	for _, mb := range mailboxes {
		reg := models.GrokRegistration{
			Email:     mb.Email,
			MailboxID: mb.ID,
			Password:  grokreg.GenPassword(16),
			Status:    "registering",
			Note:      fmt.Sprintf("来源: 邮箱管理 #%d，自动读取验证码", mb.ID),
		}
		var existing models.GrokRegistration
		if err := p.db.Where("email = ?", mb.Email).First(&existing).Error; err == nil {
			existing.MailboxID = mb.ID
			existing.Status = "registering"
			existing.Shipped = false
			existing.Password = reg.Password
			existing.Note = reg.Note
			existing.AuthData = ""
			existing.Shot = nil
			if err := p.db.Save(&existing).Error; err != nil {
				return started, err
			}
			reg = existing
		} else if err := p.db.Create(&reg).Error; err != nil {
			return started, err
		}
		started = append(started, reg)
		go p.run(reg.ID)
	}
	if len(started) == 0 {
		return nil, fmt.Errorf("账号管理和邮箱管理里都没有可用于 Grok 注册的账号")
	}
	return started, nil
}

func (p *Producer) SubmitCode(id uint, code string) error {
	code = strings.TrimSpace(code)
	if code == "" {
		return fmt.Errorf("验证码不能为空")
	}
	p.mu.Lock()
	ch := p.waiters[id]
	p.mu.Unlock()
	if ch == nil {
		return fmt.Errorf("该任务当前不在等待验证码")
	}
	select {
	case ch <- code:
		p.appendLog(id, "已收到页面提交的验证码")
		return nil
	default:
		return fmt.Errorf("验证码已提交，请等待任务继续")
	}
}

func (p *Producer) Stop(id uint) {
	p.mu.Lock()
	cancel := p.cancel[id]
	p.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// StopAll 请求停止所有在跑的 Grok 注册任务。
func (p *Producer) StopAll() {
	p.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(p.cancel))
	for _, cancel := range p.cancel {
		cancels = append(cancels, cancel)
	}
	p.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

// Progress 生产进度快照，供 /api/grok/produce/status 展示。
type Progress struct {
	Running    bool   `json:"running"`
	Target     int    `json:"target"`
	Pending    int    `json:"pending"`     // 待生产
	RunningNum int    `json:"running_num"` // 在跑
	Registered int    `json:"registered"`  // 已注册
	Failed     int    `json:"failed"`      // 注册失败
	Message    string `json:"message"`
}

// Snapshot 返回 Grok 生产进度：在跑数取自当前运行的任务，其余按库中状态统计。
func (p *Producer) Snapshot() Progress {
	p.mu.Lock()
	runningNum := len(p.cancel)
	target := p.target
	p.mu.Unlock()

	count := func(statuses ...string) int {
		var n int64
		p.db.Model(&models.GrokRegistration{}).Where("status IN ?", statuses).Count(&n)
		return int(n)
	}
	return Progress{
		Running:    runningNum > 0,
		Target:     target,
		Pending:    count("pending"),
		RunningNum: runningNum,
		Registered: count("registered"),
		Failed:     count("register_failed"),
		Message:    map[bool]string{true: "批量注册进行中", false: "等待新的批量任务"}[runningNum > 0],
	}
}

func (p *Producer) run(id uint) {
	ctx, cancel := context.WithCancel(context.Background())
	p.mu.Lock()
	p.cancel[id] = cancel
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		delete(p.waiters, id)
		delete(p.cancel, id)
		p.mu.Unlock()
	}()

	var reg models.GrokRegistration
	if err := p.db.First(&reg, id).Error; err != nil {
		return
	}

	// 并发闸门：并发已满时排队等待，避免多个有头浏览器同时抢 CPU 互相超时。
	if !p.acquireSlot(ctx, id) {
		p.appendLog(id, "已取消（排队等待空闲注册槽位时被停止）")
		p.db.Model(&models.GrokRegistration{}).Where("id = ?", id).Updates(map[string]any{
			"status": "register_failed",
			"note":   "已取消",
		})
		return
	}
	defer p.releaseSlot()

	p.appendLog(id, "开始 Grok 邮箱注册")
	// since 在获得槽位后再取，避免排队期间旧验证码被误读。
	since := time.Now().Add(-30 * time.Second)

	in := grokreg.Input{
		Email:    reg.Email,
		Password: reg.Password,
		Proxy:    p.nextProxy(),
		// Match the reference project: Grok registration is headed by default.
		// A dedicated opt-in setting can still enable headless for diagnostics.
		Headless: p.getSetting("grok_headless") == "1",
		// 协议注册为默认路径：只借浏览器签发 Turnstile 令牌，拿到后立即退出、
		// 其余全走 HTTP/gRPC。设置 grok_engine=browser 可回退到旧的全程浏览器流程。
		Engine:              p.getSetting("grok_engine"),
		Impersonate:         p.getSetting("grok_impersonate"),
		ImpersonateFallback: p.getSetting("grok_impersonate_fallback"),
		FlareSolverrURL:     p.getSetting("grok_flaresolverr_url"),
		ClearanceProxy:      p.getSetting("grok_clearance_proxy"),
		ClearanceURLs:       p.getSetting("grok_clearance_urls"),
		Log: func(f string, a ...any) {
			p.appendLog(id, fmt.Sprintf(f, a...))
		},
		WaitCode: func(ctx context.Context) (string, error) {
			if reg.MailboxID != 0 {
				return p.fetchCode(ctx, id, reg.MailboxID, since)
			}
			return p.waitManualCode(ctx, id)
		},
		SaveShot: func(png []byte) {
			p.db.Model(&models.GrokRegistration{}).Where("id = ?", id).Update("shot", png)
		},
	}

	res, err := grokreg.Register(ctx, in)
	if err != nil {
		p.appendLog(id, "注册失败: "+err.Error())
		p.db.Model(&models.GrokRegistration{}).Where("id = ?", id).Updates(map[string]any{
			"status": "register_failed",
			"note":   truncateStr(err.Error(), 500),
		})
		return
	}

	authBytes, _ := json.MarshalIndent(res.AuthJSON, "", "  ")
	p.appendLog(id, "Grok 注册成功")
	p.db.Model(&models.GrokRegistration{}).Where("id = ?", id).Updates(map[string]any{
		"status":    "registered",
		"auth_data": string(authBytes),
	})
}

func (p *Producer) waitManualCode(ctx context.Context, id uint) (string, error) {
	ch := make(chan string, 1)
	p.mu.Lock()
	p.waiters[id] = ch
	p.mu.Unlock()
	p.db.Model(&models.GrokRegistration{}).Where("id = ?", id).Update("status", "waiting_code")

	timer := time.NewTimer(codeWaitTimeout)
	defer timer.Stop()
	select {
	case code := <-ch:
		p.db.Model(&models.GrokRegistration{}).Where("id = ?", id).Update("status", "registering")
		return code, nil
	case <-timer.C:
		return "", fmt.Errorf("等待验证码超时")
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

func (p *Producer) fetchCode(ctx context.Context, id, mailboxID uint, since time.Time) (string, error) {
	var mb models.Mailbox
	if err := p.db.First(&mb, mailboxID).Error; err != nil {
		return "", fmt.Errorf("读取邮箱凭据失败: %w", err)
	}
	acc := mailfetch.Account{Email: mb.Email, ClientID: mb.ClientID, RefreshToken: mb.RefreshToken}
	deadline := time.Now().Add(codePollTimeout)
	p.appendLog(id, "开始自动读取 Grok 邮件验证码")
	for time.Now().Before(deadline) {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		msgs, err := p.mail.ListMessages(ctx, acc, 15)
		if err == nil {
			for _, m := range msgs {
				if m.ReceivedAt.Before(since) || !looksLikeGrok(m) {
					continue
				}
				if code := extractGrokCode(m.Subject); code != "" {
					p.appendLog(id, "已从邮件标题读取验证码并自动提交")
					return code, nil
				}
				full, gerr := p.mail.GetMessage(ctx, acc, m.ID)
				if gerr != nil {
					continue
				}
				if code := extractGrokCode(full.Subject + " " + full.Text); code != "" {
					p.appendLog(id, "已从邮件正文读取验证码并自动提交")
					return code, nil
				}
			}
		} else {
			p.appendLog(id, "读取邮件暂时失败，继续重试: "+err.Error())
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(codePollInterval):
		}
	}
	return "", fmt.Errorf("超时未收到 Grok 验证码邮件")
}

func extractGrokCode(s string) string {
	if code := xaiCodeRe.FindStringSubmatch(s); code != nil {
		return strings.ToUpper(code[1] + code[2])
	}
	if code := digitCodeRe.FindStringSubmatch(s); code != nil {
		return code[1]
	}
	return ""
}

func looksLikeGrok(m mailfetch.Message) bool {
	s := strings.ToLower(m.From + " " + m.FromName + " " + m.Subject)
	return strings.Contains(s, "grok") ||
		strings.Contains(s, "x.ai") ||
		strings.Contains(s, "xai") ||
		strings.Contains(s, "security code") ||
		strings.Contains(s, "verification")
}

func (p *Producer) appendLog(id uint, line string) {
	stamp := time.Now().Format("2006-01-02 15:04:05")
	var reg models.GrokRegistration
	if err := p.db.Select("log").First(&reg, id).Error; err != nil {
		return
	}
	log := reg.Log
	if strings.TrimSpace(log) == "" {
		log = ""
	} else if !strings.HasSuffix(log, "\n") {
		log += "\n"
	}
	log += stamp + " " + line + "\n"
	if len(log) > maxLogBytes {
		log = log[len(log)-maxLogBytes:]
	}
	p.db.Model(&models.GrokRegistration{}).Where("id = ?", id).Update("log", log)
}

// maxConcurrency 读取并发上限：优先用 Grok 专用键 grok_max_concurrency，
// 未设置时继承设置页「最大并发数」(max_concurrency)，都没有则默认 1，最小为 1。
func (p *Producer) maxConcurrency() int {
	raw := strings.TrimSpace(p.getSetting("grok_max_concurrency"))
	if raw == "" {
		raw = strings.TrimSpace(p.getSetting("max_concurrency"))
	}
	n := defaultMaxConcurrency
	if raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			n = parsed
		}
	}
	if n < 1 {
		n = 1
	}
	return n
}

// acquireSlot 阻塞直到并发未满（获得槽位返回 true）或 ctx 取消（返回 false）。
// 限额从设置动态读取，改大后新任务无需重启即可生效。
func (p *Producer) acquireSlot(ctx context.Context, id uint) bool {
	logged := false
	for {
		if ctx.Err() != nil {
			return false
		}
		limit := p.maxConcurrency()
		p.mu.Lock()
		if p.active < limit {
			p.active++
			p.mu.Unlock()
			return true
		}
		p.mu.Unlock()
		if !logged {
			p.appendLog(id, "并发已满，排队等待空闲注册槽位")
			logged = true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(1 * time.Second):
		}
	}
}

// releaseSlot 释放一个并发槽位。
func (p *Producer) releaseSlot() {
	p.mu.Lock()
	if p.active > 0 {
		p.active--
	}
	p.mu.Unlock()
}

func (p *Producer) getSetting(key string) string {
	var s models.Setting
	if err := p.db.Where("key = ?", key).First(&s).Error; err != nil {
		return ""
	}
	return s.Value
}

func (p *Producer) nextProxy() string {
	// Grok 跟随设置页上的全局代理开关与代理列表，不再有独立的 Grok 开关。
	enabled := strings.TrimSpace(p.getSetting("proxy_enabled"))
	raw := p.getSetting("proxy_list")
	if enabled != "1" {
		return ""
	}
	proxies := proxyList(raw)
	if len(proxies) == 0 {
		return ""
	}
	p.mu.Lock()
	proxy := proxies[p.pxIdx%len(proxies)]
	p.pxIdx++
	p.mu.Unlock()
	return proxyutil.WithBestGoTaskSession(proxy)
}

func proxyList(raw string) []string {
	raw = strings.ReplaceAll(raw, ",", "\n")
	var out []string
	for _, line := range strings.Split(raw, "\n") {
		if s := strings.TrimSpace(line); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
