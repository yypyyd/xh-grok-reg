// Package grokoauth 把 Grok Web 的 sso cookie 换成 xAI Build OAuth 令牌。
//
// 走 xAI 的设备码流程（device code → verify → approve → token），与 sub2api 的
// 「Grok Web SSO 批量导入」一致：sub2api 的 Grok 账号只有 oauth 类型、CLIProxyAPI（CPA）
// 的 xAI 凭证也要 access_token / refresh_token，光有 sso cookie 两边都导不进去。
// 注册成功后立即换一次并存入 AuthData 的 oauth 字段，导出时直接用；旧账号导出时再补换。
package grokoauth

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"

	"xh-grok-reg/internal/proxyutil"
)

const (
	oauthIssuer = "https://auth.x.ai"
	accountsURL = "https://accounts.x.ai/"
	deviceURL   = oauthIssuer + "/oauth2/device/code"
	verifyURL   = oauthIssuer + "/oauth2/device/verify"
	approveURL  = oauthIssuer + "/oauth2/device/approve"
	tokenURL    = oauthIssuer + "/oauth2/token"

	// clientID / scope / cliBaseURL 与 sub2api 的 xAI Build 客户端保持一致，
	// 否则换出来的令牌 sub2api 认不了。
	clientID    = "b1a00492-073a-47ea-816f-4c329264a828"
	buildScope  = "openid profile email offline_access grok-cli:access api:access conversations:read conversations:write"
	cliBaseURL  = "https://cli-chat-proxy.grok.com/v1"
	cpaRedirect = "http://127.0.0.1:56121/callback"
	defaultUA   = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
	maxBody     = 2 << 20
	defaultTTL  = 6 * time.Hour
	pollDeadlin = 75 * time.Second
)

// ErrUnauthorized 表示 sso 已失效（accounts.x.ai 把请求打回登录页）。
var ErrUnauthorized = errors.New("sso 已失效")

// TokenInfo 是换到的 Build OAuth 令牌及其 JWT 里的账号信息。
type TokenInfo struct {
	AccessToken  string
	RefreshToken string
	IDToken      string
	TokenType    string
	Scope        string
	ExpiresAt    int64
	Email        string
	Subject      string
	TeamID       string
}

// ConvertSSO 用 sso cookie 跑一遍设备码流程换取 Build OAuth 令牌。
// proxyRaw 为空时直连；建议传注册时用的同一个代理，保持出口 IP 一致。
func ConvertSSO(ctx context.Context, proxyRaw, sso string) (*TokenInfo, error) {
	sso = strings.TrimSpace(sso)
	if sso == "" {
		return nil, ErrUnauthorized
	}
	transport, err := proxyutil.Transport(proxyRaw)
	if err != nil {
		return nil, err
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	seedSSO(jar, sso)
	var rt http.RoundTripper
	if transport != nil {
		rt = transport
	}
	client := &http.Client{
		Transport: rt,
		Jar:       jar,
		Timeout:   90 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > 8 {
				return errors.New("xAI OAuth 重定向次数过多")
			}
			if !trustedURL(req.URL.String()) {
				return errors.New("xAI OAuth 重定向到不可信域名")
			}
			return nil
		},
	}

	status, finalURL, _, err := do(ctx, client, http.MethodGet, accountsURL, nil)
	if err != nil {
		return nil, err
	}
	if status == http.StatusUnauthorized || strings.Contains(finalURL, "sign-in") || strings.Contains(finalURL, "sign-up") {
		return nil, ErrUnauthorized
	}
	if status < 200 || status >= 400 {
		return nil, fmt.Errorf("校验 sso 失败：HTTP %d", status)
	}

	status, _, body, err := do(ctx, client, http.MethodPost, deviceURL, url.Values{
		"client_id": {clientID},
		"scope":     {buildScope},
	})
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("申请设备码失败：HTTP %d", status)
	}
	var device struct {
		DeviceCode              string `json:"device_code"`
		UserCode                string `json:"user_code"`
		VerificationURIComplete string `json:"verification_uri_complete"`
		Interval                int    `json:"interval"`
		ExpiresIn               int    `json:"expires_in"`
	}
	if err = json.Unmarshal(body, &device); err != nil {
		return nil, fmt.Errorf("解析设备码响应失败: %w", err)
	}
	if device.DeviceCode == "" || device.UserCode == "" || !trustedURL(device.VerificationURIComplete) {
		return nil, errors.New("设备码响应不完整")
	}
	if device.Interval <= 0 {
		device.Interval = 5
	}

	if status, _, _, err = do(ctx, client, http.MethodGet, device.VerificationURIComplete, nil); err != nil {
		return nil, err
	}
	if status < 200 || status >= 400 {
		return nil, fmt.Errorf("打开设备授权页失败：HTTP %d", status)
	}

	status, finalURL, _, err = do(ctx, client, http.MethodPost, verifyURL, url.Values{"user_code": {device.UserCode}})
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 400 || !strings.Contains(finalURL, "consent") {
		return nil, fmt.Errorf("设备码校验未进入授权确认页：HTTP %d", status)
	}

	status, finalURL, _, err = do(ctx, client, http.MethodPost, approveURL, url.Values{
		"user_code":      {device.UserCode},
		"action":         {"allow"},
		"principal_type": {"User"},
		"principal_id":   {""},
	})
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 400 || !strings.Contains(finalURL, "done") {
		return nil, fmt.Errorf("设备码授权未完成：HTTP %d", status)
	}

	return pollToken(ctx, client, device.DeviceCode, time.Duration(device.Interval)*time.Second)
}

func pollToken(ctx context.Context, client *http.Client, deviceCode string, interval time.Duration) (*TokenInfo, error) {
	if interval < time.Second {
		interval = time.Second
	}
	deadline := time.Now().Add(pollDeadlin)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(interval):
		}
		status, _, body, err := do(ctx, client, http.MethodPost, tokenURL, url.Values{
			"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
			"client_id":   {clientID},
			"device_code": {deviceCode},
		})
		if err != nil {
			return nil, err
		}
		var payload struct {
			AccessToken      string `json:"access_token"`
			RefreshToken     string `json:"refresh_token"`
			IDToken          string `json:"id_token"`
			TokenType        string `json:"token_type"`
			ExpiresIn        int64  `json:"expires_in"`
			Scope            string `json:"scope"`
			Error            string `json:"error"`
			ErrorDescription string `json:"error_description"`
		}
		if err = json.Unmarshal(body, &payload); err != nil {
			return nil, fmt.Errorf("解析令牌响应失败: %w", err)
		}
		if status >= 200 && status < 300 && payload.AccessToken != "" {
			if payload.ExpiresIn <= 0 {
				payload.ExpiresIn = int64(defaultTTL.Seconds())
			}
			if payload.TokenType == "" {
				payload.TokenType = "Bearer"
			}
			info := &TokenInfo{
				AccessToken:  payload.AccessToken,
				RefreshToken: payload.RefreshToken,
				IDToken:      payload.IDToken,
				TokenType:    payload.TokenType,
				Scope:        payload.Scope,
				ExpiresAt:    time.Now().Add(time.Duration(payload.ExpiresIn) * time.Second).Unix(),
			}
			applyClaims(info, payload.IDToken)
			applyClaims(info, payload.AccessToken)
			return info, nil
		}
		switch payload.Error {
		case "authorization_pending":
			continue
		case "slow_down":
			interval += 5 * time.Second
		case "access_denied", "expired_token":
			return nil, errors.New("设备授权被拒绝或已过期")
		default:
			reason := strings.TrimSpace(payload.ErrorDescription)
			if reason == "" {
				reason = payload.Error
			}
			return nil, fmt.Errorf("轮询令牌失败：HTTP %d %s", status, reason)
		}
	}
	return nil, errors.New("轮询令牌超时")
}

// SSOFromAuth 从注册产出的 auth JSON 里取 sso token：优先 sso 字段，否则回退扮 cookies。
func SSOFromAuth(auth map[string]any) string {
	if v, _ := auth["sso"].(string); strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v)
	}
	list, _ := auth["cookies"].([]any)
	for _, item := range list {
		cookie, _ := item.(map[string]any)
		if name, _ := cookie["name"].(string); name == "sso" {
			if v, _ := cookie["value"].(string); strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
			}
		}
	}
	return ""
}

// Credentials 把令牌拼成 sub2api 里 Grok oauth 账号的 credentials。
func Credentials(info *TokenInfo, email string) map[string]any {
	creds := map[string]any{
		"access_token": info.AccessToken,
		"expires_at":   time.Unix(info.ExpiresAt, 0).UTC().Format(time.RFC3339),
		"client_id":    clientID,
		"base_url":     cliBaseURL,
	}
	put := func(k, v string) {
		if strings.TrimSpace(v) != "" {
			creds[k] = v
		}
	}
	put("refresh_token", info.RefreshToken)
	put("id_token", info.IDToken)
	put("token_type", info.TokenType)
	put("scope", info.Scope)
	put("sub", info.Subject)
	put("team_id", info.TeamID)
	if strings.TrimSpace(info.Email) != "" {
		creds["email"] = info.Email
	} else {
		put("email", email)
	}
	return creds
}

// CPAAuth 把令牌拼成 CLIProxyAPI（CPA）auth-dir 里的 xAI 凭证文件。
func CPAAuth(info *TokenInfo, email string) map[string]any {
	if strings.TrimSpace(info.Email) != "" {
		email = info.Email
	}
	expiresIn := int64(0)
	if info.ExpiresAt > 0 {
		if d := info.ExpiresAt - time.Now().Unix(); d > 0 {
			expiresIn = d
		}
	}
	if expiresIn == 0 {
		expiresIn = int64(defaultTTL.Seconds())
	}
	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	auth := map[string]any{
		"type":           "xai",
		"access_token":   info.AccessToken,
		"refresh_token":  info.RefreshToken,
		"token_type":     info.TokenType,
		"expires_in":     expiresIn,
		"expired":        time.Unix(info.ExpiresAt, 0).UTC().Format("2006-01-02T15:04:05Z"),
		"last_refresh":   now,
		"email":          strings.TrimSpace(email),
		"sub":            info.Subject,
		"base_url":       cliBaseURL,
		"redirect_uri":   cpaRedirect,
		"token_endpoint": tokenURL,
		"auth_kind":      "oauth",
	}
	if strings.TrimSpace(info.IDToken) != "" {
		auth["id_token"] = info.IDToken
	}
	return auth
}

// FromStored 把存库的 credentials（Credentials 写出的那份）读回 TokenInfo，
// 给已经在注册时换好的账号直接导出用。
func FromStored(creds map[string]any) (*TokenInfo, bool) {
	str := func(k string) string { s, _ := creds[k].(string); return strings.TrimSpace(s) }
	if str("access_token") == "" {
		return nil, false
	}
	info := &TokenInfo{
		AccessToken:  str("access_token"),
		RefreshToken: str("refresh_token"),
		IDToken:      str("id_token"),
		TokenType:    str("token_type"),
		Scope:        str("scope"),
		Email:        str("email"),
		Subject:      str("sub"),
		TeamID:       str("team_id"),
	}
	if t, err := time.Parse(time.RFC3339, str("expires_at")); err == nil {
		info.ExpiresAt = t.Unix()
	}
	return info, true
}

// do 发一次请求并返回状态码、最终 URL（跟随重定向后）和响应体。
func do(ctx context.Context, client *http.Client, method, endpoint string, form url.Values) (int, string, []byte, error) {
	if !trustedURL(endpoint) {
		return 0, "", nil, errors.New("xAI OAuth 域名不可信")
	}
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return 0, endpoint, nil, err
	}
	req.Header.Set("Accept", "application/json, text/html;q=0.9, */*;q=0.8")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	req.Header.Set("User-Agent", defaultUA)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, endpoint, nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return resp.StatusCode, endpoint, nil, err
	}
	final := endpoint
	if resp.Request != nil && resp.Request.URL != nil {
		final = resp.Request.URL.String()
	}
	return resp.StatusCode, final, data, nil
}

// seedSSO 把 sso cookie 预置到 accounts.x.ai 与 auth.x.ai。
func seedSSO(jar http.CookieJar, token string) {
	for _, raw := range []string{accountsURL, oauthIssuer + "/"} {
		target, err := url.Parse(raw)
		if err != nil {
			continue
		}
		jar.SetCookies(target, []*http.Cookie{
			{Name: "sso", Value: token, Path: "/", Secure: true, HttpOnly: true},
			{Name: "sso-rw", Value: token, Path: "/", Secure: true, HttpOnly: true},
		})
	}
}

// trustedURL 只允许 https 的 x.ai 及其子域，避免跟着重定向把 sso 发到别处。
func trustedURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Scheme != "https" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host == "x.ai" || strings.HasSuffix(host, ".x.ai")
}

// applyClaims 从 JWT 里补齐 email / sub / team_id。
func applyClaims(info *TokenInfo, token string) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return
	}
	var claims map[string]any
	if json.Unmarshal(payload, &claims) != nil {
		return
	}
	str := func(k string) string { s, _ := claims[k].(string); return strings.TrimSpace(s) }
	if info.Email == "" {
		info.Email = str("email")
	}
	if info.Subject == "" {
		info.Subject = str("sub")
	}
	if info.TeamID == "" {
		info.TeamID = str("team_id")
	}
}
