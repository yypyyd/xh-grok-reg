package handlers

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"xh-grok-reg/internal/grokoauth"
	"xh-grok-reg/internal/models"
	"xh-grok-reg/internal/proxyutil"

	"github.com/gin-gonic/gin"
)

var errGrokNoSSO = errors.New("缺少 sso token")

type exportBundle struct {
	ExportedAt string          `json:"exported_at"`
	Proxies    []any           `json:"proxies"`
	Accounts   []exportAccount `json:"accounts"`
}

type exportAccount struct {
	Name        string         `json:"name"`
	Platform    string         `json:"platform"`
	Type        string         `json:"type"`
	Credentials map[string]any `json:"credentials"`
}

type grokStartInput struct {
	Email string `json:"email" binding:"required"`
	Note  string `json:"note"`
}

type grokProduceInput struct {
	Count int `json:"count" binding:"required"`
}

type grokCodeInput struct {
	Code string `json:"code" binding:"required"`
}

func (h *Handler) BrowserStatus(c *gin.Context) {
	if h.Browser == nil {
		c.JSON(http.StatusOK, gin.H{"ready": false, "phase": "unavailable"})
		return
	}
	c.JSON(http.StatusOK, h.Browser.Snapshot())
}

func validGrokStatus(s string) bool {
	return s == "" || s == "pending" || s == "registering" ||
		s == "waiting_code" || s == "registered" || s == "register_failed" ||
		s == "already_registered"
}

func (h *Handler) GrokList(c *gin.Context) {
	var regs []models.GrokRegistration
	q := h.DB.Order("updated_at desc, id desc")
	if s := c.Query("status"); s != "" {
		q = q.Where("status = ?", s)
	}
	if kw := c.Query("q"); kw != "" {
		like := "%" + kw + "%"
		q = q.Where("email LIKE ? OR note LIKE ?", like, like)
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
	q.Model(&models.GrokRegistration{}).Count(&total)
	if err := q.Offset((page - 1) * size).Limit(size).Find(&regs).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	for i := range regs {
		regs[i].AuthData = ""
		regs[i].Log = ""
	}
	c.JSON(http.StatusOK, gin.H{"data": regs, "total": total, "page": page, "size": size})
}

func (h *Handler) GrokStart(c *gin.Context) {
	var in grokStartInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if h.Browser == nil || !h.Browser.Ready() {
		c.JSON(http.StatusConflict, gin.H{"error": "缺少浏览器，无法注册：浏览器正在下载或下载失败"})
		return
	}
	reg, err := h.GrokProducer.Start(in.Email, in.Note)
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusAccepted, reg)
}

func (h *Handler) GrokProduce(c *gin.Context) {
	var in grokProduceInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if h.Browser == nil || !h.Browser.Ready() {
		c.JSON(http.StatusConflict, gin.H{"error": "缺少浏览器，无法注册：浏览器正在下载或下载失败"})
		return
	}
	regs, err := h.GrokProducer.StartFromAccounts(in.Count)
	if err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusAccepted, gin.H{"ok": true, "started": len(regs), "data": regs})
}

// GrokProduceStatus 返回 Grok 生产进度（待生产/在跑/已注册/失败）。
func (h *Handler) GrokProduceStatus(c *gin.Context) {
	c.JSON(http.StatusOK, h.GrokProducer.Snapshot())
}

// GrokProduceStop 停止所有在跑的 Grok 注册任务。
func (h *Handler) GrokProduceStop(c *gin.Context) {
	h.GrokProducer.StopAll()
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *Handler) GrokSubmitCode(c *gin.Context) {
	id64, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	var in grokCodeInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if err := h.GrokProducer.SubmitCode(uint(id64), in.Code); err != nil {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *Handler) GrokStop(c *gin.Context) {
	id64, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	h.GrokProducer.Stop(uint(id64))
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *Handler) GrokDelete(c *gin.Context) {
	id64, _ := strconv.ParseUint(c.Param("id"), 10, 64)
	h.GrokProducer.Stop(uint(id64))
	if err := h.DB.Delete(&models.GrokRegistration{}, c.Param("id")).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *Handler) GrokDeleteAll(c *gin.Context) {
	var regs []models.GrokRegistration
	h.DB.Select("id").Where("status IN ?", []string{"registering", "waiting_code"}).Find(&regs)
	for _, reg := range regs {
		h.GrokProducer.Stop(reg.ID)
	}
	r := h.DB.Where("1 = 1").Delete(&models.GrokRegistration{})
	if r.Error != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": r.Error.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": r.RowsAffected})
}

func (h *Handler) GrokLog(c *gin.Context) {
	var reg models.GrokRegistration
	if err := h.DB.First(&reg, c.Param("id")).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	c.JSON(http.StatusOK, gin.H{
		"email": reg.Email, "status": reg.Status,
		"note": reg.Note, "log": reg.Log,
		"has_shot": len(reg.Shot) > 0,
	})
}

func (h *Handler) GrokShot(c *gin.Context) {
	var reg models.GrokRegistration
	if err := h.DB.First(&reg, c.Param("id")).Error; err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
		return
	}
	if len(reg.Shot) == 0 {
		c.JSON(http.StatusNotFound, gin.H{"error": "暂无异常截图"})
		return
	}
	c.Data(http.StatusOK, "image/png", reg.Shot)
}

// GrokDownload 下载选中 Grok 账号。
// format=console：导出 grok2api 的 Grok Console 账号导入 JSON（sso_token 列表）。
// format=grok2api：导出 grok2api 的 sso token 池（ssoBasic）。
// format=sub2api：导出 Sub2API 账号备份 JSON（accounts 数组，platform=grok、type=oauth）。
// format=cpa：导出 CLIProxyAPI auth-dir 的 xAI 凭证（每账号一个 xai-<邮箱>.json，
// 单账号直接返 JSON，多账号返 ZIP）。
// 这两种都用注册时存下的 OAuth 令牌；旧账号没存就当场拿 sso 补换。
// 请求体：{ "ids": [1,2,3], "format": "console|grok2api|sub2api|cpa", "unshipped_only": false }。
// unshipped_only=true 时忽略 ids，导出全部已注册且未出库的账号。
func (h *Handler) GrokDownload(c *gin.Context) {
	var in struct {
		IDs           []uint `json:"ids"`
		Format        string `json:"format"`
		UnshippedOnly bool   `json:"unshipped_only"`
	}
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if len(in.IDs) == 0 && !in.UnshippedOnly {
		c.JSON(http.StatusBadRequest, gin.H{"error": "未选择 Grok 账号"})
		return
	}
	if in.Format == "" {
		in.Format = "console"
	}
	if in.Format != "console" && in.Format != "grok2api" && in.Format != "sub2api" && in.Format != "cpa" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "不支持的导出格式"})
		return
	}
	var regs []models.GrokRegistration
	q := h.DB.Where("status = ? AND auth_data <> ''", "registered")
	if in.UnshippedOnly {
		q = q.Where("shipped = ?", false)
	} else {
		q = q.Where("id IN ?", in.IDs)
	}
	if err := q.Find(&regs).Error; err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}
	if len(regs) == 0 {
		if in.UnshippedOnly {
			c.JSON(http.StatusBadRequest, gin.H{"error": "没有已注册未出库的 Grok 账号"})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "所选 Grok 账号没有可下载的会话数据"})
		return
	}

	switch in.Format {
	case "grok2api":
		h.downloadGrokPool(c, regs)
	case "sub2api", "cpa":
		h.downloadGrokOAuth(c, regs, in.Format)
	default:
		h.downloadGrokConsole(c, regs)
	}
}

// grokSSOToken 取出账号的 sso token：优先用注册时提取的 sso 字段，否则回退扫描 cookie。
func grokSSOToken(authData string) string {
	var auth map[string]any
	_ = json.Unmarshal([]byte(authData), &auth)
	return grokoauth.SSOFromAuth(auth)
}

// grokStoredOAuth 取出注册时已换好并存库的 OAuth 令牌（AuthData 里的 oauth 字段）。
func grokStoredOAuth(authData string) (*grokoauth.TokenInfo, bool) {
	var auth map[string]any
	_ = json.Unmarshal([]byte(authData), &auth)
	creds, _ := auth["oauth"].(map[string]any)
	if creds == nil {
		return nil, false
	}
	return grokoauth.FromStored(creds)
}

// downloadGrokConsole 导出 grok2api 的 Grok Console 账号导入 JSON。
func (h *Handler) downloadGrokConsole(c *gin.Context, regs []models.GrokRegistration) {
	accounts := make([]map[string]any, 0, len(regs))
	ids := make([]uint, 0, len(regs))
	for _, r := range regs {
		sso := grokSSOToken(r.AuthData)
		if sso == "" {
			continue
		}
		accounts = append(accounts, map[string]any{
			"name":      "Grok Console " + r.Email,
			"email":     r.Email,
			"sso_token": sso,
		})
		ids = append(ids, r.ID)
	}
	if len(accounts) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "所选 Grok 账号没有可用的 sso token"})
		return
	}
	h.DB.Model(&models.GrokRegistration{}).Where("id IN ?", ids).Update("shipped", true)

	out, _ := json.MarshalIndent(map[string]any{"provider": "console", "accounts": accounts}, "", "  ")
	name := "grok_console_token.json"
	if len(accounts) == 1 {
		name = "grok-console-" + safeFileName(regs[0].Email) + ".json"
	}
	c.Header("Content-Disposition", `attachment; filename="`+name+`"`)
	c.Data(http.StatusOK, "application/json; charset=utf-8", out)
}

// downloadGrokPool 导出 grok2api 的 sso token 池（ssoBasic）。
func (h *Handler) downloadGrokPool(c *gin.Context, regs []models.GrokRegistration) {
	pool := make([]map[string]any, 0, len(regs))
	ids := make([]uint, 0, len(regs))
	for _, r := range regs {
		sso := grokSSOToken(r.AuthData)
		if sso == "" {
			continue
		}
		pool = append(pool, map[string]any{
			"token": sso,
			"tags":  []string{"auto-register"},
			"note":  r.Email,
		})
		ids = append(ids, r.ID)
	}
	if len(pool) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "所选 Grok 账号没有可用的 sso token"})
		return
	}
	h.DB.Model(&models.GrokRegistration{}).Where("id IN ?", ids).Update("shipped", true)

	out, _ := json.MarshalIndent(map[string]any{"ssoBasic": pool}, "", "  ")
	name := "grok2api_token.json"
	if len(pool) == 1 {
		name = "grok2api-" + safeFileName(regs[0].Email) + ".json"
	}
	c.Header("Content-Disposition", `attachment; filename="`+name+`"`)
	c.Data(http.StatusOK, "application/json; charset=utf-8", out)
}

// grokOAuthWorkers 限制同时补换 sso→OAuth 的数量：每个账号要走一遍设备码流程
// （含轮询等待），并发太高既拖慢又容易被 xAI 限流。注册时已换好的账号不占用它。
const grokOAuthWorkers = 3

// downloadGrokOAuth 导出依赖 OAuth 令牌的两种格式：
// format=sub2api 出 Sub2API 账号备份 JSON，format=cpa 出 CLIProxyAPI 的 xAI 凭证。
// 优先用注册时存库的令牌；旧账号没有就现场拿 sso 补换一次。
func (h *Handler) downloadGrokOAuth(c *gin.Context, regs []models.GrokRegistration, format string) {
	proxyRaw := h.exportProxy()
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Minute)
	defer cancel()

	type result struct {
		info  *grokoauth.TokenInfo
		id    uint
		email string
		err   error
	}
	results := make([]result, len(regs))
	sem := make(chan struct{}, grokOAuthWorkers)
	var wg sync.WaitGroup
	for i, r := range regs {
		if info, ok := grokStoredOAuth(r.AuthData); ok {
			results[i] = result{info: info, id: r.ID, email: r.Email}
			continue
		}
		sso := grokSSOToken(r.AuthData)
		if sso == "" {
			results[i] = result{email: r.Email, err: errGrokNoSSO}
			continue
		}
		wg.Add(1)
		go func(i int, reg models.GrokRegistration, sso string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			info, cerr := grokoauth.ConvertSSO(ctx, proxyRaw, sso)
			if cerr != nil {
				results[i] = result{email: reg.Email, err: cerr}
				return
			}
			h.saveGrokOAuth(reg, info)
			results[i] = result{info: info, id: reg.ID, email: reg.Email}
		}(i, r, sso)
	}
	wg.Wait()

	ok := make([]result, 0, len(regs))
	ids := make([]uint, 0, len(regs))
	failed := make([]string, 0)
	for _, res := range results {
		if res.err != nil {
			failed = append(failed, res.email+"("+res.err.Error()+")")
			continue
		}
		ok = append(ok, res)
		ids = append(ids, res.id)
	}
	if len(ok) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "没有账号能拿到 Grok OAuth 令牌：" + strings.Join(failed, "；")})
		return
	}
	h.DB.Model(&models.GrokRegistration{}).Where("id IN ?", ids).Update("shipped", true)
	if len(failed) > 0 { // 页面读不到响应体里的提示，用响应头带回跳过的账号数
		c.Header("X-Export-Skipped", strconv.Itoa(len(failed)))
	}

	if format == "cpa" {
		files := make([]exportFile, 0, len(ok))
		for _, res := range ok {
			out, _ := json.MarshalIndent(grokoauth.CPAAuth(res.info, res.email), "", "  ")
			files = append(files, exportFile{name: "xai-" + safeFileName(res.email) + ".json", data: out})
		}
		writeExportFiles(c, files, "grok-cpa-xai.zip")
		return
	}

	accounts := make([]exportAccount, 0, len(ok))
	for _, res := range ok {
		accounts = append(accounts, exportAccount{
			Name:        res.email,
			Platform:    "grok",
			Type:        "oauth",
			Credentials: grokoauth.Credentials(res.info, res.email),
		})
	}
	out, _ := json.MarshalIndent(exportBundle{
		ExportedAt: time.Now().UTC().Format(time.RFC3339),
		Proxies:    []any{},
		Accounts:   accounts,
	}, "", "  ")
	name := "grok-sub2api.json"
	if len(accounts) == 1 {
		name = "grok-sub2api-" + safeFileName(ok[0].email) + ".json"
	}
	c.Header("Content-Disposition", `attachment; filename="`+name+`"`)
	c.Data(http.StatusOK, "application/json; charset=utf-8", out)
}

// exportFile 是一个待下载文件：单个直接返回，多个打包成 ZIP。
type exportFile struct {
	name string
	data []byte
}

func writeExportFiles(c *gin.Context, files []exportFile, zipName string) {
	if len(files) == 1 {
		c.Header("Content-Disposition", `attachment; filename="`+files[0].name+`"`)
		c.Data(http.StatusOK, "application/json; charset=utf-8", files[0].data)
		return
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range files {
		entry, err := zw.Create(f.name)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "创建压缩包失败"})
			return
		}
		if _, err = entry.Write(f.data); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "写入压缩包失败"})
			return
		}
	}
	if err := zw.Close(); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "完成压缩包失败"})
		return
	}
	c.Header("Content-Disposition", `attachment; filename="`+zipName+`"`)
	c.Data(http.StatusOK, "application/zip", buf.Bytes())
}

// saveGrokOAuth 把现场补换到的令牌写回 AuthData 的 oauth 字段，下次导出不用再换。
func (h *Handler) saveGrokOAuth(reg models.GrokRegistration, info *grokoauth.TokenInfo) {
	var auth map[string]any
	if json.Unmarshal([]byte(reg.AuthData), &auth) != nil || auth == nil {
		return
	}
	auth["oauth"] = grokoauth.Credentials(info, reg.Email)
	out, err := json.MarshalIndent(auth, "", "  ")
	if err != nil {
		return
	}
	h.DB.Model(&models.GrokRegistration{}).Where("id = ?", reg.ID).Update("auth_data", string(out))
}

// exportProxy 取设置页代理列表里的第一个代理；未开代理则直连。
// 与注册一致地为 BestGo 动态住宅代理挂任务级 session，避免一次换取里出口 IP 乱跳。
func (h *Handler) exportProxy() string {
	var enabled models.Setting
	if err := h.DB.Where("key = ?", "proxy_enabled").First(&enabled).Error; err != nil ||
		strings.TrimSpace(enabled.Value) != "1" {
		return ""
	}
	var list models.Setting
	if err := h.DB.Where("key = ?", "proxy_list").First(&list).Error; err != nil {
		return ""
	}
	for _, line := range strings.Split(strings.ReplaceAll(list.Value, ",", "\n"), "\n") {
		if s := strings.TrimSpace(line); s != "" {
			return proxyutil.WithBestGoTaskSession(proxyutil.Normalize(s))
		}
	}
	return ""
}

func safeFileName(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "account"
	}
	repl := func(r rune) rune {
		if r < 32 {
			return '_'
		}
		switch r {
		case '/', '\\', ':', '*', '?', '"', '<', '>', '|':
			return '_'
		default:
			return r
		}
	}
	return strings.Map(repl, s)
}
