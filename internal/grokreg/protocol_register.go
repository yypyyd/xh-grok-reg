package grokreg

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"xh-grok-reg/internal/grokreg/clearance"
	"xh-grok-reg/internal/grokreg/protocol"
)

// signupConfigTTL 是注册配置（sitekey / Next-Action id / router state tree）的
// 缓存时长。这些值跟着 x.ai 发版才变，批量注册时每个号重抓一遍要多花十几秒。
const signupConfigTTL = 20 * time.Minute

var signupCfg struct {
	mu  sync.Mutex
	cfg protocol.SignupConfig
	at  time.Time
}

// cachedSignupConfig 返回仍在有效期内的注册配置。
func cachedSignupConfig() (protocol.SignupConfig, bool) {
	signupCfg.mu.Lock()
	defer signupCfg.mu.Unlock()
	if signupCfg.cfg.ActionID == "" || time.Since(signupCfg.at) > signupConfigTTL {
		return protocol.SignupConfig{}, false
	}
	return signupCfg.cfg, true
}

// storeSignupConfig 在注册成功后记下这套配置，供后续账号复用。
func storeSignupConfig(cfg protocol.SignupConfig) {
	if cfg.ActionID == "" || cfg.StateTree == "" {
		return
	}
	signupCfg.mu.Lock()
	signupCfg.cfg, signupCfg.at = cfg, time.Now()
	signupCfg.mu.Unlock()
}

// invalidateSignupConfig 丢弃缓存，让下一个号重新抓取。
func invalidateSignupConfig() {
	signupCfg.mu.Lock()
	signupCfg.cfg, signupCfg.at = protocol.SignupConfig{}, time.Time{}
	signupCfg.mu.Unlock()
}

// registerProtocol drives the whole Grok signup over HTTP/gRPC using a
// Chrome-impersonating TLS client. A browser is spawned only to mint the
// single-use Cloudflare Turnstile token (via the CloakBrowser helper), which
// exits as soon as the token is issued — everything else is protocol traffic.
func registerProtocol(ctx context.Context, in Input) (*Result, error) {
	in.logf("Grok 协议注册启动（浏览器仅用于签发 Turnstile 令牌）")
	if strings.TrimSpace(in.Proxy) != "" {
		server, _, _, perr := parseProxy(in.Proxy)
		if perr != nil {
			return nil, fmt.Errorf("解析代理失败: %w", perr)
		}
		in.logf("Grok 协议客户端使用上游代理: %s", server)
	}

	impersonate := firstNonEmpty(in.Impersonate, "chrome_131")
	fallbacks := protocol.FallbackProfiles(in.ImpersonateFallback)
	fsURL := strings.TrimSpace(in.FlareSolverrURL)

	client, err := protocol.NewClientOpts(protocol.ClientOptions{
		Proxy:               in.Proxy,
		Impersonate:         impersonate,
		ImpersonateFallback: fallbacks,
	})
	if err != nil {
		return nil, fmt.Errorf("创建协议客户端失败: %w", err)
	}

	// FetchConfig scrapes sitekey / Next-Action id / router state tree at runtime.
	// Protocol-first: on Cloudflare block try fingerprint fallbacks, then (when
	// configured) fall back to a FlareSolverr clearance bundle.
	var cm *clearance.Manager
	var scfg protocol.SignupConfig
	// 抢到缓存时只做一次 warm（养 cookie + 探 Cloudflare），省掉抓 JS chunk
	// 找 Next-Action id 的开销；warm 不过或无缓存时走完整拓。
	if cached, ok := cachedSignupConfig(); ok {
		if status, _, werr := client.WarmSignup(); werr == nil && status == 200 {
			scfg = cached
			in.logf("复用缓存的注册配置 site_key=%s action=%s…", scfg.SiteKey, trimText(scfg.ActionID, 12))
		} else {
			in.logf("缓存配置 warm 失败(status=%d err=%v)，重新抓取", status, werr)
			invalidateSignupConfig()
		}
	}
	if scfg.ActionID == "" {
		scfg, err = client.FetchConfig()
	}
	if err != nil {
		in.logf("⚠ 首选指纹 warm 失败(profile=%s): %v，尝试指纹回退", client.Profile(), err)
		tried := map[string]struct{}{client.Profile(): {}}
		for _, fb := range fallbacks {
			if _, ok := tried[fb]; ok {
				continue
			}
			tried[fb] = struct{}{}
			if rerr := client.RecreateWithProfile(fb); rerr != nil {
				continue
			}
			in.logf("尝试指纹回退 profile=%s", fb)
			if scfg, err = client.FetchConfig(); err == nil {
				in.logf("warm 成功 profile=%s", client.Profile())
				break
			}
			in.logf("回退 %s 仍失败: %v", fb, err)
		}
	}
	if err != nil && fsURL != "" {
		in.logf("⚠ 协议直连仍被 Cloudflare 拦截，启用 FlareSolverr clearance 兜底")
		cm = clearance.NewManager(fsURL, strings.TrimSpace(in.ClearanceProxy), in.ClearanceURLs)
		if msg, perr := cm.Prewarm(); perr != nil {
			in.logf("clearance 预热异常: %v | %s", perr, msg)
		} else {
			in.logf("clearance 预热完成: %s", msg)
		}
		client, err = protocol.NewClientOpts(protocol.ClientOptions{
			Proxy:       in.Proxy,
			Clearance:   cm,
			Impersonate: client.Profile(),
		})
		if err != nil {
			return nil, fmt.Errorf("创建协议客户端(clearance)失败: %w", err)
		}
		scfg, err = client.FetchConfig()
	}
	if err != nil {
		return nil, fmt.Errorf("获取注册配置失败: %w", err)
	}
	in.logf("注册配置就绪 site_key=%s action=%s… source=%s profile=%s",
		scfg.SiteKey, trimText(scfg.ActionID, 12), scfg.Source, client.Profile())

	// Chromium cannot authenticate to an upstream proxy via --proxy-server, so an
	// authenticated proxy needs a loopback bridge for the Turnstile mint. The TLS
	// client itself talks to the authenticated proxy directly.
	in.mintProxy = normalizeProxy(in.Proxy)
	if bridge, addr := maybeAuthBridge(in.Proxy); bridge != nil {
		defer bridge.Close()
		in.mintProxy = addr
	}

	// 立即开始并行签发 Turnstile 令牌（有效期约 5 分钟），与取码、等码完全
	// 重叠，把这一步从关键路径上拿掉。
	type mintOutcome struct {
		token string
		err   error
	}
	mintCh := make(chan mintOutcome, 1)
	mintCtx, cancelMint := context.WithCancel(ctx)
	defer cancelMint()
	go func() {
		tok, merr := mintTurnstileToken(mintCtx, in, scfg.SiteKey, protocol.SiteURL+"/sign-up")
		mintCh <- mintOutcome{token: tok, err: merr}
	}()

	// 1) request email validation code, wait for the mailbox to deliver it.
	if err := client.CreateEmailCode(in.Email); err != nil {
		return nil, fmt.Errorf("发送邮箱验证码失败: %w", err)
	}
	in.logf("已请求邮箱验证码，等待收码…")

	code, err := in.WaitCode(ctx)
	if err != nil {
		return nil, fmt.Errorf("等待验证码失败: %w", err)
	}
	code = strings.TrimSpace(code)

	client.ClearAuthCookies()
	if err := client.VerifyEmailCode(in.Email, code); err != nil {
		return nil, fmt.Errorf("校验邮箱验证码失败: %w", err)
	}
	// ValidatePassword mirrors the browser form's field 4/5 probe; non-fatal.
	if err := client.ValidatePassword(in.Email, in.Password); err != nil {
		in.logf("ValidatePassword 跳过/失败(非致命): %v", err)
	}

	// 2) collect the token minted in parallel, then submit signup.
	mint := <-mintCh
	if mint.err != nil {
		return nil, fmt.Errorf("签发 Turnstile 令牌失败: %w", mint.err)
	}
	token := mint.token
	in.logf("Turnstile 令牌已就绪(len=%d)，转协议注册", len(token))

	body := protocol.BuildSignupBody(in.Email, in.Password, code, token)
	text, sso, err := client.SignupServerAction(body, scfg.ActionID, scfg.StateTree)
	if sso == "" {
		sso = protocol.ExtractSSOFromText(text)
	}
	if sso == "" {
		// action id / state tree 可能已随 x.ai 发版失效，丢控缓存让下一个号重新抓。
		invalidateSignupConfig()
		if err != nil {
			return nil, fmt.Errorf("协议注册失败: %w", err)
		}
		return nil, fmt.Errorf("协议注册未返回会话 SSO")
	}
	storeSignupConfig(scfg)
	in.logf("注册成功，已获得会话 SSO")

	auth := map[string]any{
		"auth_mode":   "grok_protocol_session",
		"platform":    "grok",
		"email":       in.Email,
		"password":    in.Password,
		"first_name":  in.FirstName,
		"last_name":   in.LastName,
		"captured_at": time.Now().UTC().Format(time.RFC3339),
		"sso":         sso,
		"cookies": []map[string]any{{
			"name":   "sso",
			"value":  sso,
			"domain": ".x.ai",
			"path":   "/",
		}},
	}
	return &Result{AuthJSON: auth}, nil
}

// maybeAuthBridge starts a loopback proxy bridge only when the upstream proxy
// carries credentials (Chromium can use an unauthenticated proxy directly).
func maybeAuthBridge(raw string) (*localAuthProxyBridge, string) {
	if strings.TrimSpace(raw) == "" {
		return nil, ""
	}
	u, err := url.Parse(normalizeProxy(raw))
	if err != nil || u.User == nil {
		return nil, ""
	}
	if pass, hasPass := u.User.Password(); !hasPass && pass == "" && u.User.Username() == "" {
		return nil, ""
	}
	bridge, addr, berr := startLocalAuthProxyBridge(raw)
	if berr != nil {
		return nil, ""
	}
	return bridge, addr
}
