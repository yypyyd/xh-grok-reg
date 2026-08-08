package livecheck

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// xAI OAuth 默认参数（与 internal/grokreg 注册时铸造 CPA 凭证所用一致）。
const (
	defaultXaiTokenEndpoint = "https://auth.x.ai/oauth2/token"
	defaultXaiClientID      = "b1a00492-073a-47ea-816f-4c329264a828"
)

// GrokItem 一个待测活的 Grok 账号：ID + sso token（Console 探测用），
// 以及从 cpa_xai 取出的 refresh_token 及可选端点/客户端（回退用）。
type GrokItem struct {
	ID            uint
	SSO           string
	RefreshToken  string
	TokenEndpoint string
	ClientID      string
}

// GrokResult 一个账号的测活结果：三态状态 + Console 额度摘要（无则为空）。
type GrokResult struct {
	Status string
	Quota  string
}

// GrokChunk 是 Grok 批量测活的增量回调：每完成一个账号就回传结果。
type GrokChunk func(map[uint]GrokResult)

// CheckGrok 批量测活 Grok 账号，不需要浏览器：
// 有 sso token 时探测 Grok Console 的 /v1/usage，直接得到 chat/image/video 额度；
// 没有 sso 的旧账号回退到用 xAI OAuth refresh_token 做一次授权。
//
//	Console usage 200 / refresh 返回 access_token -> alive
//	401 或明确的认证错误                          -> dead
//	Cloudflare / 429 / 5xx / 超时 / 无可用凭据     -> unknown
func CheckGrok(ctx context.Context, items []GrokItem, onChunk GrokChunk) map[uint]GrokResult {
	out := make(map[uint]GrokResult, len(items))
	if len(items) == 0 {
		return out
	}
	client := &http.Client{Timeout: 20 * time.Second}

	var (
		mu  sync.Mutex
		wg  sync.WaitGroup
		sem = make(chan struct{}, 5)
	)
	record := func(id uint, res GrokResult) {
		mu.Lock()
		out[id] = res
		mu.Unlock()
		if onChunk != nil {
			onChunk(map[uint]GrokResult{id: res})
		}
	}

	for _, it := range items {
		if ctx.Err() != nil {
			record(it.ID, GrokResult{Status: StatusUnknown})
			continue
		}
		if strings.TrimSpace(it.SSO) == "" && strings.TrimSpace(it.RefreshToken) == "" {
			// 既没有 sso 也没有 refresh_token 时无法验证，判 unknown。
			record(it.ID, GrokResult{Status: StatusUnknown})
			continue
		}
		wg.Add(1)
		go func(it GrokItem) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			record(it.ID, probeGrokOne(ctx, client, it))
		}(it)
	}
	wg.Wait()
	return out
}

func probeGrokOne(ctx context.Context, client *http.Client, it GrokItem) GrokResult {
	if strings.TrimSpace(it.SSO) != "" {
		// Console 是导出目标，优先探测它：既验活也拿到额度。
		usageCtx, cancel := context.WithTimeout(ctx, 40*time.Second)
		defer cancel()
		usage := ProbeConsole(usageCtx, client, it.SSO)
		if usage.Status != StatusUnknown || strings.TrimSpace(it.RefreshToken) == "" {
			return GrokResult{Status: usage.Status, Quota: usage.Summary()}
		}
	}
	return GrokResult{Status: probeGrokRefresh(ctx, client, it)}
}

// probeGrokRefresh 用 xAI OAuth refresh_token 做一次授权，作为无 sso 时的回退探测。
func probeGrokRefresh(ctx context.Context, client *http.Client, it GrokItem) string {
	if strings.TrimSpace(it.RefreshToken) == "" {
		return StatusUnknown
	}
	endpoint := strings.TrimSpace(it.TokenEndpoint)
	if endpoint == "" {
		endpoint = defaultXaiTokenEndpoint
	}
	clientID := strings.TrimSpace(it.ClientID)
	if clientID == "" {
		clientID = defaultXaiClientID
	}

	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", it.RefreshToken)
	form.Set("client_id", clientID)

	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return StatusUnknown
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "grok-register-cpa/1.0")

	resp, err := client.Do(req)
	if err != nil {
		return StatusUnknown
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	switch {
	case resp.StatusCode == 200:
		var parsed struct {
			AccessToken string `json:"access_token"`
		}
		_ = json.Unmarshal(body, &parsed)
		if strings.TrimSpace(parsed.AccessToken) != "" {
			return StatusAlive
		}
		return StatusUnknown
	case resp.StatusCode == 400 || resp.StatusCode == 401:
		// 只有明确的认证类错误才判死；其它 4xx（如限流误报）保持 unknown。
		if isAuthError(body) {
			return StatusDead
		}
		return StatusUnknown
	default:
		return StatusUnknown
	}
}

// isAuthError 判断 OAuth 错误体是否属于"凭据失效"类（可判死）。
func isAuthError(body []byte) bool {
	var parsed struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(parsed.Error)) {
	case "invalid_grant", "invalid_token", "unauthorized_client", "invalid_client", "access_denied":
		return true
	default:
		return false
	}
}
