package grokreg

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	launcherflags "github.com/go-rod/rod/lib/launcher/flags"
	"github.com/go-rod/rod/lib/proto"
)

func registerBrowser(ctx context.Context, in Input) (res *Result, err error) {
	if in.Headless {
		in.logf("启动无头浏览器，打开 Grok 邮箱注册页")
	} else {
		in.logf("启动可见浏览器，打开 Grok 邮箱注册页")
	}

	// Match DrissionPage ChromiumOptions from the reference project. Rod's
	// launcher normally adds a large Puppeteer-style flag set (background timer
	// throttling, site isolation, NetworkServiceInProcess, etc.); those flags are
	// visible to managed Turnstile even when navigator.webdriver is false.
	l := launcher.New()
	for _, flag := range []string{
		"no-startup-window",
		"disable-features",
		"disable-dev-shm-usage",
		"disable-background-networking",
		"disable-background-timer-throttling",
		"disable-backgrounding-occluded-windows",
		"disable-breakpad",
		"disable-client-side-phishing-detection",
		"disable-component-extensions-with-background-pages",
		"disable-default-apps",
		"disable-hang-monitor",
		"disable-ipc-flooding-protection",
		"disable-prompt-on-repost",
		"disable-renderer-backgrounding",
		"disable-sync",
		"disable-site-isolation-trials",
		"enable-automation",
		"enable-features",
		"force-color-profile",
		"metrics-recording-only",
		"use-mock-keychain",
	} {
		l = l.Delete(launcherflags.Flag(flag))
	}
	if in.Headless {
		// Chrome 128 的 --headless 仍是旧无头（忽略扩展），必须显式 new headless
		// 才能加载 Turnstile 补丁扩展。
		l = l.Set("headless", "new")
	}
	l = l.
		NoSandbox(true).
		Set("no-default-browser-check").
		Set("disable-suggestions-ui").
		Set("no-first-run").
		Set("disable-infobars").
		Set("disable-popup-blocking").
		Set("hide-crash-restore-bubble").
		Set("disable-features", "PrivacySandboxSettings4")
	// Chrome also exposes navigator.webdriver when remote-debugging-port=0.
	// DrissionPage's auto_port() uses a random non-zero port, so do the same.
	debugPort, perr := availableLoopbackPort()
	if perr != nil {
		return nil, fmt.Errorf("分配 Chrome 调试端口失败: %w", perr)
	}
	l = l.Set("remote-debugging-port", strconv.Itoa(debugPort))
	if !in.Headless {
		in.logf("使用有头模式")
	}
	if chromePath, cerr := grokChromiumBin(); cerr != nil {
		in.logf("准备 Grok 专用 Chromium 失败，回退默认浏览器: %v", cerr)
	} else {
		l = l.Bin(chromePath)
		in.logf("使用 Grok 专用 Chromium(%d)，与默认浏览器隔离", launcher.RevisionDefault)
	}

	// Load the Turnstile-Patch extension like the reference project. It rewrites
	// MouseEvent.screenX/screenY at document_start in every frame so x.ai's
	// invisible managed Turnstile issues a token to a real checkbox click — no
	// third-party solver required. It also exposes window.__cfSolve, which the
	// minted-token injection needs, so it is loaded in headless (new) mode too.
	if patchDir, perr := extractTurnstilePatch(); perr != nil {
		in.logf("释放 Turnstile 补丁扩展失败，回退到无扩展模式: %v", perr)
	} else {
		defer os.RemoveAll(patchDir)
		l = l.Set("disable-extensions-except", patchDir).Set("load-extension", patchDir)
		in.logf("已加载 Turnstile 补丁扩展")
	}

	var authBridge *localAuthProxyBridge
	proxyConfigured := strings.TrimSpace(in.Proxy) != ""
	if proxyConfigured {
		server, user, pass, perr := parseProxy(in.Proxy)
		if perr != nil {
			return nil, fmt.Errorf("解析代理失败: %w", perr)
		}
		if user != "" || pass != "" {
			upstreamServer := server
			authBridge, server, perr = startLocalAuthProxyBridge(in.Proxy)
			if perr != nil {
				return nil, fmt.Errorf("启动认证代理桥失败: %w", perr)
			}
			defer authBridge.Close()
			in.logf("已启用 Chromium 本地认证代理桥，本地 %s → 上游 %s", server, upstreamServer)
		}
		l = l.Set("proxy-server", server)
		in.logf("Chromium 使用代理入口: %s", server)
		// The Turnstile mint reuses this loopback endpoint so its token is signed
		// from the same egress IP as the registration.
		in.mintProxy = server
	}

	controlURL, err := l.Launch()
	if err != nil {
		return nil, fmt.Errorf("启动 Chrome 失败: %w", err)
	}
	// Rod otherwise emulates a fixed Chrome 114 macOS laptop on every page.
	// The reference project uses the actual installed Chromium device profile.
	browser := rod.New().NoDefaultDevice().ControlURL(controlURL)
	if err = browser.Connect(); err != nil {
		return nil, fmt.Errorf("连接 Chrome 失败: %w", err)
	}
	defer func() {
		// 关浏览器后清理 launcher 临时用户数据目录，避免残留 Profile 堆积
		_ = rod.Try(browser.MustClose)
		l.Cleanup()
	}()

	var page *rod.Page
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("Grok 注册流程异常: %v", r)
		}
		if err == nil || page == nil || in.SaveShot == nil {
			return
		}
		func() {
			defer func() {
				if r2 := recover(); r2 != nil {
					in.logf("截图失败(panic): %v", r2)
				}
			}()
			data, serr := page.Timeout(15*time.Second).Screenshot(false, nil)
			if serr != nil || len(data) == 0 {
				in.logf("截图失败: %v", serr)
				return
			}
			in.SaveShot(data)
			in.logf("已保存失败现场截图")
		}()
	}()
	if proxyConfigured {
		checkPage := browser.MustPage("https://api.ipify.org?format=json")
		checkPage.MustWaitLoad()
		if body, berr := checkPage.Timeout(15 * time.Second).Element("body"); berr == nil && body != nil {
			if value, terr := body.Text(); terr == nil {
				in.logf("Chromium 实际代理出口: %s", trimText(value, 160))
			}
		}
		_ = checkPage.Close()
	}

	geo := lookupGeoIPViaRequest(in)
	acceptLang := "en-US,en;q=0.9"
	if geo != nil {
		_, acceptLang = localeForCountry(geo.CountryCode)
	}

	// Match the reference project: use the browser's native fingerprint instead
	// of mixing a Windows UA/platform with a Linux Chrome/TLS/WebGL stack.
	page = browser.MustPage("")
	trace := startProtocolTrace(page)
	_ = (proto.EmulationSetDeviceMetricsOverride{
		Width:             1280,
		Height:            900,
		DeviceScaleFactor: 1,
		Mobile:            false,
	}).Call(page)
	if in.Headless {
		// 无头 Chrome 的 UA 带 HeadlessChrome 标记，会被 Cloudflare 直接拦，改回普通 Chrome。
		if ver, verr := (proto.BrowserGetVersion{}).Call(browser); verr == nil {
			if ua := cleanUserAgent(ver.UserAgent); ua != "" {
				_ = (proto.EmulationSetUserAgentOverride{
					UserAgent:      ua,
					AcceptLanguage: acceptLang,
					Platform:       "Linux x86_64",
				}).Call(page)
				in.logf("无头 UA 已修正: %s", ua)
			}
		}
	}
	in.logf("浏览器语言: %s", acceptLang)
	if geo != nil {
		applyGeo(page, geo, in)
	}

	nav := page.Timeout(120 * time.Second)
	nav.MustNavigate("https://accounts.x.ai/sign-up?redirect=grok-com&return_to=%2F")
	nav.MustWaitLoad()
	if fp, ferr := page.Eval(`() => JSON.stringify({ua:navigator.userAgent,platform:navigator.platform,webdriver:navigator.webdriver})`); ferr == nil {
		in.logf("原生浏览器指纹: %s", fp.Value.Str())
	}
	// A full-page challenge must be completed before registration can continue.
	if err = waitForCloudflare(ctx, page, in, 10*time.Minute); err != nil {
		return nil, err
	}
	in.logf("Grok 注册页已加载")

	if err = fillGrokEmail(ctx, page, in); err != nil {
		return nil, err
	}
	in.logf("已提交邮箱，等待安全代码输入框")

	// After email submit CF may block again.
	if err = waitForSelectorOrCF(ctx, page, in, trace, "input[name='code']", 90*time.Second); err != nil {
		return nil, fmt.Errorf("%w protocol=%s", err, trace.diag())
	}
	in.logf("安全代码输入框已出现，等待验证码")

	code, err := in.WaitCode(ctx)
	if err != nil {
		return nil, fmt.Errorf("等待验证码失败: %w", err)
	}
	code = strings.TrimSpace(code)
	if code == "" {
		return nil, fmt.Errorf("验证码为空")
	}

	if err = submitSecurityCode(page, in, code); err != nil {
		return nil, err
	}
	in.logf("已提交安全代码，等待 Grok 会话")

	if err = waitGrokReady(ctx, page, in); err != nil {
		return nil, err
	}
	in.logf("Grok 会话已就绪，正在提取浏览器会话")

	auth, err := captureAuth(page, in)
	if err != nil {
		return nil, err
	}

	// 附加后处理：提取 sso token（Console / Sub2API 导出用）。
	if sso := ssoFromCookies(auth); sso != "" {
		auth["sso"] = sso
	}

	in.logf("Grok 注册完成")
	return &Result{AuthJSON: auth}, nil
}

// ssoFromCookies 取出 Grok 的 sso cookie 值，供 Console / Sub2API 导出使用。
func ssoFromCookies(auth map[string]any) string {
	list, _ := auth["cookies"].([]map[string]any)
	for _, c := range list {
		if name, _ := c["name"].(string); name == "sso" {
			if v, _ := c["value"].(string); v != "" {
				return v
			}
		}
	}
	return ""
}

func availableLoopbackPort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	if err = ln.Close(); err != nil {
		return 0, err
	}
	return port, nil
}

type protocolRequest struct {
	ID          proto.NetworkRequestID `json:"-"`
	URL         string                 `json:"url"`
	Method      string                 `json:"method"`
	Type        string                 `json:"type"`
	ContentType string                 `json:"contentType,omitempty"`
	PostBytes   int                    `json:"postBytes,omitempty"`
	Status      int                    `json:"status,omitempty"`
	MimeType    string                 `json:"mimeType,omitempty"`
	Response    string                 `json:"response,omitempty"`
	Failure     string                 `json:"failure,omitempty"`
}

type protocolTrace struct {
	page     *rod.Page
	mu       sync.Mutex
	requests []*protocolRequest
	byID     map[proto.NetworkRequestID]*protocolRequest
	console  []string
}

// startProtocolTrace observes the exact Castle and ConnectRPC traffic without
// modifying fetch/XMLHttpRequest. This is intentionally CDP-only so anti-abuse
// SDKs cannot detect a monkey-patched browser API.
func startProtocolTrace(page *rod.Page) *protocolTrace {
	t := &protocolTrace{page: page, byID: make(map[proto.NetworkRequestID]*protocolRequest)}
	go page.EachEvent(
		func(e *proto.NetworkRequestWillBeSent) {
			if e.Request == nil || (e.Type != proto.NetworkResourceTypeFetch && e.Type != proto.NetworkResourceTypeXHR) {
				return
			}
			r := &protocolRequest{
				ID:          e.RequestID,
				URL:         e.Request.URL,
				Method:      e.Request.Method,
				Type:        string(e.Type),
				ContentType: networkHeader(e.Request.Headers, "content-type"),
				PostBytes:   len(e.Request.PostData),
			}
			t.mu.Lock()
			if len(t.requests) < 40 {
				t.requests = append(t.requests, r)
				t.byID[e.RequestID] = r
			}
			t.mu.Unlock()
		},
		func(e *proto.NetworkResponseReceived) {
			if e.Response == nil {
				return
			}
			t.mu.Lock()
			if r := t.byID[e.RequestID]; r != nil {
				r.Status = e.Response.Status
				r.MimeType = e.Response.MIMEType
			}
			t.mu.Unlock()
		},
		func(e *proto.NetworkLoadingFailed) {
			t.mu.Lock()
			if r := t.byID[e.RequestID]; r != nil {
				r.Failure = e.ErrorText
				if e.BlockedReason != "" {
					r.Failure += " blocked=" + string(e.BlockedReason)
				}
			}
			t.mu.Unlock()
		},
		func(e *proto.NetworkLoadingFinished) {
			t.mu.Lock()
			_, tracked := t.byID[e.RequestID]
			t.mu.Unlock()
			if !tracked {
				return
			}
			go func(id proto.NetworkRequestID) {
				body, err := (proto.NetworkGetResponseBody{RequestID: id}).Call(page)
				if err != nil || body == nil {
					return
				}
				t.mu.Lock()
				if r := t.byID[id]; r != nil {
					r.Response = traceLimit(body.Body, 800)
				}
				t.mu.Unlock()
			}(e.RequestID)
		},
		func(e *proto.RuntimeConsoleAPICalled) {
			if e.Type != proto.RuntimeConsoleAPICalledTypeError && e.Type != proto.RuntimeConsoleAPICalledTypeWarning {
				return
			}
			parts := make([]string, 0, len(e.Args))
			for _, arg := range e.Args {
				if arg == nil {
					continue
				}
				value := arg.Description
				if !arg.Value.Nil() {
					value = arg.Value.String()
				}
				if value != "" {
					parts = append(parts, value)
				}
			}
			line := traceLimit(string(e.Type)+": "+strings.Join(parts, " "), 1200)
			t.mu.Lock()
			if len(t.console) < 20 {
				t.console = append(t.console, line)
			}
			t.mu.Unlock()
		},
	)()
	return t
}

func (t *protocolTrace) diag() string {
	t.mu.Lock()
	requests := make([]protocolRequest, 0, len(t.requests))
	for _, r := range t.requests {
		if r != nil && (strings.Contains(r.URL, "/auth_mgmt.") || strings.Contains(strings.ToLower(r.URL), "castle")) {
			requests = append(requests, *r)
		}
	}
	console := append([]string(nil), t.console...)
	t.mu.Unlock()
	b, err := json.Marshal(map[string]any{"requests": requests, "console": console})
	if err != nil {
		return err.Error()
	}
	return string(b)
}

func (t *protocolTrace) terminalError() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, r := range t.requests {
		if r == nil || !strings.Contains(r.URL, "/auth_mgmt.AuthManagement/CreateEmailValidationCode") {
			continue
		}
		if r.Status == 403 {
			return "xAI 验证码协议接口返回 HTTP 403：Castle 令牌已生成且 ConnectRPC 请求已发出，但当前出口 IP/浏览器会话被 Cloudflare WAF 拒绝"
		}
		if r.Status >= 400 {
			return fmt.Sprintf("xAI 验证码协议接口返回 HTTP %d", r.Status)
		}
		if r.Failure != "" {
			return "xAI 验证码协议请求失败: " + r.Failure
		}
	}
	return ""
}

func networkHeader(headers proto.NetworkHeaders, name string) string {
	for key, value := range headers {
		if strings.EqualFold(key, name) {
			return strings.Trim(value.String(), `"`)
		}
	}
	return ""
}

func traceLimit(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "...[truncated]"
}

func cleanUserAgent(ua string) string {
	ua = strings.TrimSpace(ua)
	ua = strings.ReplaceAll(ua, "HeadlessChrome", "Chrome")
	// Some builds append "; Headless" tokens.
	ua = strings.ReplaceAll(ua, "Headless", "")
	ua = strings.Join(strings.Fields(ua), " ")
	if !strings.Contains(ua, "Chrome/") {
		return userAgent
	}
	return ua
}

// waitForSelectorOrCF waits for a selector and pauses for visible Cloudflare checks.
func waitForSelectorOrCF(ctx context.Context, page *rod.Page, in Input, trace *protocolTrace, selector string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		pg := page.Timeout(5 * time.Second)
		has, el, err := pg.Has(selector)
		if err == nil && has && el != nil {
			if vis, _ := el.Visible(); vis {
				return nil
			}
		}
		// password form is also progress after email/code in some paths
		if selector == "input[name='code']" {
			if hasPW, _, _ := pg.Has("input[type='password']"); hasPW {
				return nil
			}
		}
		if trace != nil {
			if reason := trace.terminalError(); reason != "" {
				return fmt.Errorf("%s", reason)
			}
		}
		// only block on interactive/full-page CF — empty hidden response is normal
		if isCFChallengePage(pg) || captchaInteractive(pg) {
			if err := waitForCloudflare(ctx, page, in, timeout); err != nil {
				return err
			}
			continue
		}
		// surface page errors early (rate limit, invalid email, etc.)
		if msg := pageBlockReason(pg); msg != "" {
			return fmt.Errorf("%s", msg)
		}
		time.Sleep(800 * time.Millisecond)
	}
	return fmt.Errorf("等待元素超时: %s diag=%s", selector, captchaDiag(page.Timeout(5*time.Second)))
}

// pageBlockReason returns a short Chinese error when the signup page is blocked.
func pageBlockReason(page *rod.Page) string {
	v, err := page.Eval(`() => {
		const t = (document.body && document.body.innerText || '').replace(/\s+/g, ' ');
		if (/Too many validation codes/i.test(t)) return '该邮箱验证码发送过于频繁，请换邮箱或稍后再试';
		if (/Retry in/i.test(t) && /validation code|security code/i.test(t)) return '验证码发送受限，请稍后重试';
		if (/already (exists|registered)|already have an account/i.test(t)) return '该邮箱已注册 Grok';
		if (/invalid email|email.*invalid/i.test(t)) return '邮箱格式无效或不可用';
		if (/couldn.?t (send|verify)|failed to send/i.test(t)) return '验证码发送失败';
		if (/rate limit|too many requests|try again later/i.test(t)) return '请求过于频繁，被限流';
		if (/invalid|expired|错误|无效|过期/i.test(t) && /code|验证/i.test(t)) return '验证码无效或已过期';
		return '';
	}`)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(v.Value.Str())
}

// waitForCloudflare keeps the browser in front while the user completes Turnstile.
// It deliberately does not click the widget or inject a response token.
func waitForCloudflare(ctx context.Context, page *rod.Page, in Input, timeout time.Duration) error {
	if !pendingCaptcha(page) && !isCFChallengePage(page) {
		return nil
	}
	if in.Headless {
		in.logf("检测到 Cloudflare 人机验证；当前为无头模式，等待验证自动完成")
	} else {
		in.logf("请在打开的 Chrome 窗口中完成 Cloudflare 人机验证；通过后流程会自动继续")
	}
	_, _ = page.Activate()
	_ = (proto.PageBringToFront{}).Call(page)
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		pg := page.Timeout(5 * time.Second)
		if captchaSolved(pg) {
			in.logf("Cloudflare 人机验证已通过")
			time.Sleep(800 * time.Millisecond)
			return nil
		}
		if !pendingCaptcha(pg) && !isCFChallengePage(pg) {
			in.logf("Cloudflare 检查已结束")
			return nil
		}
		time.Sleep(time.Second)
	}
	return fmt.Errorf("等待 Cloudflare 人机验证超时（%s）", timeout)
}

func captchaDiag(page *rod.Page) string {
	v, err := page.Eval(`() => {
		const iframes = [...document.querySelectorAll('iframe')].map(f => ({
			src: (f.src || '').slice(0, 80),
			title: f.title || '',
			w: Math.round(f.getBoundingClientRect().width),
			h: Math.round(f.getBoundingClientRect().height),
		}));
		const resp = document.querySelector('input[name="cf-turnstile-response"], textarea[name="cf-turnstile-response"]');
		const sitekey = document.querySelector('[data-sitekey]');
		const buttons = [...document.querySelectorAll('button')].map(b => ({
			text: (b.innerText || '').replace(/\s+/g, ' ').trim().slice(0, 60),
			type: b.type || '',
			disabled: !!b.disabled,
			visible: !!(b.offsetWidth || b.offsetHeight || b.getClientRects().length),
		})).filter(b => b.visible).slice(0, 8);
		const text = (document.body.innerText || '').replace(/\s+/g, ' ').slice(0, 120);
		return JSON.stringify({
			url: location.href.slice(0, 100),
			webdriver: navigator.webdriver,
			ua: navigator.userAgent.slice(0, 120),
			hasResp: !!resp,
			respLen: resp ? (resp.value || '').length : 0,
			sitekey: sitekey ? sitekey.getAttribute('data-sitekey') : '',
			iframes,
			buttons,
			text,
		});
	}`)
	if err != nil {
		return err.Error()
	}
	return v.Value.Str()
}

func isCFChallengePage(page *rod.Page) bool {
	ok, err := page.Eval(`() => {
		const t = (document.title || '') + ' ' + (document.body && document.body.innerText || '');
		if (/Just a moment|Checking your browser|Attention Required|Enable JavaScript and cookies/i.test(t)) return true;
		if (document.querySelector('#challenge-running, #challenge-stage, #cf-challenge-running, .cf-browser-verification')) return true;
		const u = location.href || '';
		return /cdn-cgi\/challenge|__cf_chl/i.test(u);
	}`)
	if err != nil {
		return false
	}
	return ok.Value.Bool()
}

func clickSubmit(page *rod.Page, in Input) error {
	buttons, err := page.Timeout(10 * time.Second).Elements(`button[type="submit"], button`)
	if err != nil {
		return fmt.Errorf("查找提交按钮: %w", err)
	}

	var firstVisible, typedSubmit, labeledSubmit *rod.Element
	var firstLabel, typedLabel, labeledLabel string
	for _, button := range buttons {
		visible, err := button.Visible()
		if err != nil || !visible {
			continue
		}
		label, _ := button.Text()
		label = strings.Join(strings.Fields(label), " ")
		lowerLabel := strings.ToLower(label)
		if strings.Contains(lowerLabel, "cookie") ||
			strings.Contains(lowerLabel, "reject") ||
			strings.Contains(lowerLabel, "cancel") ||
			strings.Contains(lowerLabel, "signing into") ||
			lowerLabel == "go back" {
			continue
		}
		if firstVisible == nil {
			firstVisible = button
			firstLabel = label
		}
		switch lowerLabel {
		case "sign up", "continue", "verify", "submit", "next", "send code", "create account":
			labeledSubmit = button
			labeledLabel = label
		}
		typeAttr, _ := button.Attribute("type")
		if typedSubmit == nil && typeAttr != nil && strings.EqualFold(*typeAttr, "submit") {
			typedSubmit = button
			typedLabel = label
		}
	}

	submit, selectedLabel := labeledSubmit, labeledLabel
	if submit == nil {
		submit, selectedLabel = typedSubmit, typedLabel
	}
	if submit == nil {
		submit, selectedLabel = firstVisible, firstLabel
	}
	if submit == nil {
		return fmt.Errorf("未找到可见的提交按钮")
	}
	// Match DrissionPage/reference behavior: DOM click first. CDP coordinate
	// clicks can block indefinitely on Xvfb when the compositor box is stale.
	submit = submit.CancelTimeout().Timeout(5 * time.Second)
	if _, evalErr := submit.Eval(`() => this.click()`); evalErr != nil {
		fallback := submit.CancelTimeout().Timeout(5 * time.Second)
		if !mouseClickElement(fallback) {
			return fmt.Errorf("DOM 及 CDP 点击提交按钮均失败: %w", evalErr)
		}
		in.logf("DOM 点击不可用，已使用 CDP 坐标点击回退")
	}
	in.logf("已点击按钮: %s", selectedLabel)
	return nil
}

// codeInputGone 判断安全代码输入框是否已消失（页面已前进到下一步）。
func codeInputGone(page *rod.Page) bool {
	has, _, err := page.Timeout(2 * time.Second).Has("input[name='code']")
	if err != nil {
		return false
	}
	return !has
}

// submitSecurityCode 输入并提交安全代码，容忍 x.ai 在验证码输满后自动前进的情况：
// 若在点击提交前/后代码输入框已消失，视为已提交成功，避免误报“未找到可见的提交按钮”。
func submitSecurityCode(page *rod.Page, in Input, code string) error {
	input, err := page.Timeout(10 * time.Second).Element("input[name='code']")
	if err != nil {
		if codeInputGone(page) {
			in.logf("安全代码输入框已消失，判定页面已自动前进")
			return nil
		}
		return fmt.Errorf("查找安全代码输入框失败: %w", err)
	}
	input = input.CancelTimeout().Timeout(10 * time.Second)
	if err = input.SelectAllText(); err != nil {
		if codeInputGone(page) {
			return nil
		}
		return fmt.Errorf("选中安全代码输入框失败: %w", err)
	}
	if err = input.Input(code); err != nil {
		if codeInputGone(page) {
			return nil
		}
		return fmt.Errorf("输入安全代码失败: %w", err)
	}

	// x.ai 有时在验证码输满后自动提交（页面自动前进），有时需要手动点提交按钮。
	// 重试点击；每次点击前后都检查输入框是否已消失，消失即视为已提交。
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if codeInputGone(page) {
			in.logf("安全代码已生效，页面已自动前进")
			return nil
		}
		if err = clickSubmit(page, in); err != nil {
			lastErr = err
			if codeInputGone(page) {
				in.logf("提交按钮已消失但页面已前进，判定安全代码已提交")
				return nil
			}
			time.Sleep(1 * time.Second)
			continue
		}
		// 点击成功，等待页面前进；未前进也交由后续 waitGrokReady 处理。
		for i := 0; i < 6; i++ {
			if codeInputGone(page) {
				return nil
			}
			time.Sleep(500 * time.Millisecond)
		}
		return nil
	}
	if codeInputGone(page) {
		return nil
	}
	return fmt.Errorf("点击安全代码提交按钮失败: %w", lastErr)
}

func clickEmailSignup(page *rod.Page) {
	page.Timeout(30 * time.Second).MustEval(`() => {
		const visible = el => !!(el.offsetWidth || el.offsetHeight || el.getClientRects().length);
		const btn = [...document.querySelectorAll('button')].find(b => visible(b) && /Use email|Sign up with email|使用邮箱注册/i.test(b.innerText || ''));
		if (!btn) throw new Error('email signup button not found');
		btn.click();
	}`)
}

func waitGrokReady(ctx context.Context, page *rod.Page, in Input) error {
	deadline := time.Now().Add(10 * time.Minute)
	profileFilled := false
	submitCount := 0
	lastSubmit := time.Time{}
	cfWaitSince := time.Time{}
	lastCFRetry := time.Time{}
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		pg := page.Timeout(8 * time.Second)
		info, _ := pg.Info()
		if info != nil && strings.Contains(info.URL, "grok.com") {
			return nil
		}
		// After Complete sign up, x.ai lands on the logged-in account page
		// (accounts.x.ai/account); treat that as a completed registration too.
		if info != nil && strings.Contains(info.URL, "accounts.x.ai/account") {
			in.logf("已完成注册，进入 x.ai 账户页")
			return nil
		}
		if has, _, _ := pg.Has("textarea"); has {
			return nil
		}

		if has, _, _ := pg.Has("input[type='password']"); has {
			if !profileFilled {
				in.logf("检测到 Grok 完成注册页，填写姓名和密码")
				if err := fillProfileHuman(pg, in); err != nil {
					in.logf("填写资料失败: %v", err)
					time.Sleep(2 * time.Second)
					continue
				}
				profileFilled = true
			}

			if (pendingCaptcha(pg) || isCFChallengePage(pg)) && !captchaSolved(pg) {
				if err := waitForCloudflare(ctx, page, in, time.Until(deadline)); err != nil {
					return err
				}
				// The checkbox response is now owned by the page; submit normally.
				lastSubmit = time.Time{}
			}
			cfMounted := turnstileMounted(pg)
			if cfMounted && !captchaSolved(pg) {
				if cfWaitSince.IsZero() {
					cfWaitSince = time.Now()
					in.logf("资料已填写，等待页面安全校验 token")
				}
				// Sign the Turnstile token with the CloakBrowser mint pool and
				// inject it into cf-turnstile-response so the form submits — no
				// third-party solver required. The mint blocks for a while, so
				// throttle retries.
				if time.Since(lastCFRetry) >= 12*time.Second {
					lastCFRetry = time.Now()
					sitekey := pageSitekey(pg)
					pageURL := ""
					if info != nil {
						pageURL = info.URL
					}
					in.logf("调用 CloakBrowser 令牌池签发 Turnstile token")
					// The mint blocks for tens of seconds, so pg's short deadline
					// is stale afterwards; inject with a fresh page handle.
					if token, merr := mintTurnstileToken(ctx, in, sitekey, pageURL); merr != nil {
						in.logf("令牌池签发失败，稍后重试: %v", merr)
					} else {
						cbs, fieldSet := injectMintedToken(page.Timeout(10*time.Second), token)
						in.logf("已注入令牌池 Turnstile token(len=%d 回调=%d 字段=%t)", len(token), cbs, fieldSet)
					}
				}
				time.Sleep(800 * time.Millisecond)
				continue
			}
			cfWaitSince = time.Time{}

			if captchaSolved(pg) && time.Since(lastSubmit) >= 3*time.Second {
				if clickCompleteSignup(pg) {
					submitCount++
					lastSubmit = time.Now()
					in.logf("Cloudflare 已验证，提交 Complete sign up")
				}
			} else if !cfMounted && !pendingCaptcha(pg) && time.Since(lastSubmit) >= 15*time.Second && submitCount < 2 {
				// Some deployments render Turnstile only after the first normal submit.
				if clickCompleteSignup(pg) {
					submitCount++
					lastSubmit = time.Now()
					in.logf("已提交完成注册")
				}
			}
			time.Sleep(2 * time.Second)
			continue
		}

		// A full-page interstitial can also appear between form steps.
		if isCFChallengePage(pg) || captchaInteractive(pg) {
			if err := waitForCloudflare(ctx, page, in, time.Until(deadline)); err != nil {
				return err
			}
			continue
		}

		if msg := pageBlockReason(pg); msg != "" {
			return fmt.Errorf("%s", msg)
		}
		if hasText(pg, "button", "Continue|继续|完成|确认") {
			pg.MustElementR("button", "Continue|继续|完成|确认").MustEval(`() => this.click()`)
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("等待 Grok 会话就绪超时")
}

func turnstileMounted(page *rod.Page) bool {
	ok, err := page.Eval(`() => !!(
		document.querySelector('input[name="cf-turnstile-response"], textarea[name="cf-turnstile-response"]') ||
		document.querySelector('iframe[src*="turnstile"], .cf-turnstile, [data-sitekey], script[src*="turnstile"]')
	)`)
	if err != nil {
		return false
	}
	return ok.Value.Bool()
}

// captchaInteractive is true when a clickable Turnstile widget is visible (not 1x1 managed iframe).
func captchaInteractive(page *rod.Page) bool {
	ok, err := page.Eval(`() => {
		const visible = el => {
			if (!el) return false;
			const s = getComputedStyle(el);
			const r = el.getBoundingClientRect();
			return s.display !== 'none' && s.visibility !== 'hidden' && Number(s.opacity || 1) > 0
				&& r.width >= 4 && r.height >= 4;
		};
		const text = document.body ? (document.body.innerText || '') : '';
		if (/Verify you are human|验证您是真人|确认您是真人/i.test(text)) return true;
		for (const f of document.querySelectorAll('iframe')) {
			const s = ((f.src||'') + ' ' + (f.title||'')).toLowerCase();
			const r = f.getBoundingClientRect();
			const isCF = /cloudflare|turnstile|challenge|cdn-cgi|widget containing/.test(s);
			if (visible(f) && isCF && r.width >= 100 && r.height >= 40) return true;
			if (visible(f) && !s.trim() && r.width >= 200 && r.height >= 40 && r.height <= 100) return true;
		}
		const w = document.querySelector('.cf-turnstile, [data-sitekey]');
		if (w) {
			const r = w.getBoundingClientRect();
			if (visible(w) && r.width >= 100 && r.height >= 40) return true;
		}
		return false;
	}`)
	if err != nil {
		return false
	}
	return ok.Value.Bool()
}

func waitInvisibleTurnstile(ctx context.Context, page *rod.Page, in Input, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return
		default:
		}
		if captchaSolved(page.Timeout(3 * time.Second)) {
			in.logf("不可见 Turnstile 已自动签发 token")
			return
		}
		if captchaInteractive(page.Timeout(3 * time.Second)) {
			_ = waitForCloudflare(ctx, page, in, timeout)
			return
		}
		// poke the 1x1 iframe / complete button area occasionally
		_, _ = page.Eval(`() => {
			const resp = document.querySelector('input[name="cf-turnstile-response"]');
			const host = resp && (resp.closest('form') || resp.parentElement);
			const iframe = host && host.querySelector('iframe');
			if (iframe) {
				const r = iframe.getBoundingClientRect();
				// force a reflow; some widgets expand after focus
				iframe.dispatchEvent(new Event('focus'));
				window.dispatchEvent(new Event('resize'));
				return { w: r.width, h: r.height };
			}
			return null;
		}`)
		time.Sleep(1500 * time.Millisecond)
	}
}

// grokChromiumBin returns a Chromium binary managed in a Grok-dedicated rod
// directory (browser-grok), kept separate from the service default rod browser
// so registrations never share a binary or profile. rod's --load-extension
// content-script injection works on this Chromium revision but is silently
// ignored by current stable Chrome, so the Turnstile patch only takes effect
// here.
func grokChromiumBin() (string, error) {
	b := launcher.NewBrowser()
	b.RootDir = filepath.Join(filepath.Dir(launcher.DefaultBrowserDir), "browser-grok")
	return b.Get()
}

func pendingCaptcha(page *rod.Page) bool {
	// True only when a real challenge is active — NOT merely an empty hidden token field.
	// x.ai always mounts cf-turnstile-response; treating empty as pending caused deadlocks.
	ok, err := page.Eval(`() => {
		const visible = el => {
			if (!el) return false;
			const s = getComputedStyle(el);
			const r = el.getBoundingClientRect();
			return s.display !== 'none' && s.visibility !== 'hidden' && Number(s.opacity || 1) > 0
				&& r.width >= 4 && r.height >= 4;
		};
		const text = document.body ? (document.body.innerText || '') : '';
		if (/Verify you are human|验证您是真人|确认您是真人|Just a moment|Checking your browser|Attention Required/i.test(text)) return true;
		if (document.querySelector('#challenge-running, #challenge-stage, #cf-challenge-running, .cf-browser-verification')) return true;
		for (const f of document.querySelectorAll('iframe')) {
			const s = ((f.src||'') + ' ' + (f.title||'') + ' ' + (f.name||'')).toLowerCase();
			const r = f.getBoundingClientRect();
			const isCF = /cloudflare|turnstile|challenge|cdn-cgi|widget containing/.test(s);
			// only count visible-sized widgets as pending challenge
			if (visible(f) && isCF && r.width >= 100 && r.height >= 40) return true;
			if (visible(f) && !s.trim() && r.width >= 200 && r.height >= 40 && r.height <= 100) return true;
		}
		const w = document.querySelector('.cf-turnstile, [data-sitekey]');
		if (w) {
			const r = w.getBoundingClientRect();
			if (visible(w) && r.width >= 100 && r.height >= 40) return true;
		}
		return false;
	}`)
	if err != nil {
		return false
	}
	return ok.Value.Bool()
}

func captchaSolved(page *rod.Page) bool {
	ok, err := page.Eval(`() => {
		const response = document.querySelector('input[name="cf-turnstile-response"], textarea[name="cf-turnstile-response"]');
		if (response && response.value && response.value.length > 20) return true;
		const text = document.body ? (document.body.innerText || '') : '';
		if (/Success!|验证成功|已通过/i.test(text) && !/Verify you are human|验证您是真人/i.test(text)) return true;
		// token may live in turnstile callback input without name attribute
		const anyToken = [...document.querySelectorAll('input[type="hidden"], textarea')].some(el => {
			const n = (el.name || el.id || '').toLowerCase();
			return /turnstile|cf-/.test(n) && el.value && el.value.length > 20;
		});
		return anyToken;
	}`)
	if err != nil {
		return false
	}
	return ok.Value.Bool()
}

// tryClickTurnstile attempts real mouse clicks on the Turnstile checkbox area.
// Uses CDP Input events (not element.click()).
func tryClickTurnstile(page *rod.Page, in Input) bool {
	_, _ = page.Activate()
	_ = (proto.PageBringToFront{}).Call(page)

	// Prefer rod shape of matching iframes — most reliable for off-screen windows.
	if clickTurnstileByRod(page, in) {
		return true
	}

	points, err := page.Eval(`() => {
		const rectOf = el => {
			if (!el) return null;
			const r = el.getBoundingClientRect();
			if (r.width < 4 || r.height < 4) return null;
			return { x: r.left + window.scrollX, y: r.top + window.scrollY, w: r.width, h: r.height, vx: r.left, vy: r.top };
		};
		const out = [];
		const pushBox = (r, tag) => {
			if (!r) return;
			// viewport coords for CDP mouse
			const baseX = r.vx, baseY = r.vy;
			const spots = [
				{ x: baseX + Math.min(28, Math.max(10, r.w * 0.10)), y: baseY + r.h * 0.50, tag: tag + '-cb' },
				{ x: baseX + Math.min(34, Math.max(14, r.w * 0.14)), y: baseY + r.h * 0.48, tag: tag + '-cb2' },
				{ x: baseX + 22, y: baseY + r.h * 0.5, tag: tag + '-22' },
				{ x: baseX + r.w * 0.5, y: baseY + r.h * 0.5, tag: tag + '-mid' },
			];
			for (const s of spots) out.push({ x: s.x, y: s.y, tag: s.tag, w: r.w, h: r.h });
		};
		for (const f of document.querySelectorAll('iframe')) {
			const src = (f.getAttribute('src') || '') + ' ' + (f.getAttribute('title') || '') + ' ' + (f.getAttribute('name') || '');
			const r = f.getBoundingClientRect();
			const hit = /cloudflare|turnstile|challenge|cdn-cgi|Widget containing|cf-/i.test(src)
				|| (r.width >= 200 && r.height >= 40 && r.height <= 100);
			if (hit) pushBox(rectOf(f), 'iframe');
		}
		for (const w of document.querySelectorAll('.cf-turnstile, [data-sitekey], #cf-turnstile, [id*="turnstile"], [class*="turnstile"]')) {
			pushBox(rectOf(w), 'widget');
		}
		for (const el of document.querySelectorAll('*')) {
			if (!el.shadowRoot) continue;
			const inner = el.shadowRoot.querySelector('iframe, .cf-turnstile, [data-sitekey]');
			if (inner) pushBox(rectOf(inner) || rectOf(el), 'shadow');
		}
		if (out.length === 0) {
			const body = document.body.innerText || '';
			if (/Verify you are human|验证您是真人|确认您是真人/i.test(body)) {
				const candidates = [...document.querySelectorAll('div, label, span')].filter(el => {
					const t = (el.innerText || '').trim();
					return t.length > 0 && t.length < 80 && /Verify you are human|验证您是真人|确认您是真人/i.test(t);
				});
				for (const c of candidates.slice(0, 3)) pushBox(rectOf(c), 'label');
			}
		}
		const uniq = [];
		for (const p of out) {
			if (uniq.some(u => Math.abs(u.x - p.x) < 3 && Math.abs(u.y - p.y) < 3)) continue;
			uniq.push(p);
		}
		return uniq.slice(0, 10);
	}`)
	if err != nil {
		in.logf("定位 Cloudflare 控件失败: %v", err)
		return false
	}

	raw, _ := json.Marshal(points.Value)
	var targets []struct {
		X   float64 `json:"x"`
		Y   float64 `json:"y"`
		Tag string  `json:"tag"`
		W   float64 `json:"w"`
		H   float64 `json:"h"`
	}
	if err := json.Unmarshal(raw, &targets); err != nil || len(targets) == 0 {
		return false
	}

	clicked := false
	limit := 4
	if len(targets) < limit {
		limit = len(targets)
	}
	for i := 0; i < limit; i++ {
		t := targets[i]
		jx := t.X + float64(ri(5)-2)
		jy := t.Y + float64(ri(5)-2)
		if mouseClickAt(page, jx, jy) {
			in.logf("自动点击 Cloudflare (%s @ %.0f,%.0f)", t.Tag, jx, jy)
			clicked = true
			time.Sleep(300 * time.Millisecond)
		}
	}

	// frame-internal click when same-process frames are enabled
	frames, _ := page.Elements("iframe")
	for _, f := range frames {
		src, _ := f.Attribute("src")
		title, _ := f.Attribute("title")
		s := strings.ToLower(fmt.Sprintf("%v %v", src, title))
		if !strings.Contains(s, "cloudflare") && !strings.Contains(s, "turnstile") && !strings.Contains(s, "challenge") && !strings.Contains(s, "widget containing") {
			// size heuristic for blank-src widgets
			shape, serr := f.Shape()
			if serr != nil || shape == nil || shape.Box() == nil {
				continue
			}
			box := shape.Box()
			if !(box.Width >= 200 && box.Height >= 40 && box.Height <= 100) {
				continue
			}
		}
		_ = f.ScrollIntoView()
		frame, ferr := f.Frame()
		if ferr != nil || frame == nil {
			continue
		}
		if el, e := frame.Element("input[type='checkbox'], #challenge-stage, .ctp-checkbox-label, label, body"); e == nil && el != nil {
			if mouseClickElement(el) {
				in.logf("自动点击 Cloudflare frame 内部")
				clicked = true
			}
		}
	}
	return clicked
}

func clickTurnstileByRod(page *rod.Page, in Input) bool {
	sels := []string{
		`iframe[src*="challenges.cloudflare.com"]`,
		`iframe[src*="turnstile"]`,
		`iframe[title*="Cloudflare"]`,
		`iframe[title*="Widget containing"]`,
		`iframe[src*="cdn-cgi"]`,
		`.cf-turnstile`,
		`[data-sitekey]`,
	}
	clicked := false
	for _, sel := range sels {
		els, err := page.Elements(sel)
		if err != nil || len(els) == 0 {
			continue
		}
		for _, el := range els {
			_ = el.ScrollIntoView()
			shape, serr := el.Shape()
			if serr != nil || shape == nil {
				continue
			}
			box := shape.Box()
			if box == nil || box.Width < 4 || box.Height < 4 {
				continue
			}
			// checkbox hotspot near left edge
			xs := []float64{
				box.X + minF(28, maxF(10, box.Width*0.1)),
				box.X + 22,
				box.X + box.Width*0.5,
			}
			y := box.Y + box.Height/2
			for _, x := range xs {
				if mouseClickAt(page, x+float64(ri(3)-1), y+float64(ri(3)-1)) {
					in.logf("自动点击 Cloudflare rod(%s @ %.0f,%.0f)", sel, x, y)
					clicked = true
					time.Sleep(250 * time.Millisecond)
				}
			}
		}
		if clicked {
			return true
		}
	}
	// last resort: any iframe of typical turnstile size
	frames, _ := page.Elements("iframe")
	for _, f := range frames {
		shape, err := f.Shape()
		if err != nil || shape == nil || shape.Box() == nil {
			continue
		}
		box := shape.Box()
		if box.Width < 200 || box.Height < 40 || box.Height > 100 {
			continue
		}
		_ = f.ScrollIntoView()
		x := box.X + 26
		y := box.Y + box.Height/2
		if mouseClickAt(page, x, y) {
			in.logf("自动点击疑似 Turnstile iframe @ %.0f,%.0f (%.0fx%.0f)", x, y, box.Width, box.Height)
			return true
		}
	}
	return false
}

func mouseClickAt(page *rod.Page, x, y float64) bool {
	if x < 0 || y < 0 {
		return false
	}
	mouse := page.Mouse
	if err := mouse.MoveLinear(proto.NewPoint(x, y), 8+ri(8)); err != nil {
		if err2 := mouse.MoveTo(proto.NewPoint(x, y)); err2 != nil {
			// last resort: direct CDP events
			return cdpClick(page, x, y)
		}
	}
	time.Sleep(40*time.Millisecond + time.Duration(ri(90))*time.Millisecond)
	if err := mouse.Click(proto.InputMouseButtonLeft, 1); err != nil {
		return cdpClick(page, x, y)
	}
	return true
}

func mouseClickElement(el *rod.Element) bool {
	if el == nil {
		return false
	}
	_ = el.ScrollIntoView()
	shape, err := el.Shape()
	if err != nil || shape == nil {
		return el.Click(proto.InputMouseButtonLeft, 1) == nil
	}
	pt := shape.OnePointInside()
	if pt == nil {
		if box := shape.Box(); box != nil {
			return mouseClickAt(el.Page(), box.X+box.Width/2, box.Y+box.Height/2)
		}
		return el.Click(proto.InputMouseButtonLeft, 1) == nil
	}
	return mouseClickAt(el.Page(), pt.X, pt.Y)
}

func cdpClick(page *rod.Page, x, y float64) bool {
	// Explicit CDP mouse sequence for headless reliability.
	_ = (proto.InputDispatchMouseEvent{
		Type: proto.InputDispatchMouseEventTypeMouseMoved,
		X:    x, Y: y,
	}).Call(page)
	time.Sleep(30 * time.Millisecond)
	_ = (proto.InputDispatchMouseEvent{
		Type: proto.InputDispatchMouseEventTypeMousePressed,
		X:    x, Y: y,
		Button:     proto.InputMouseButtonLeft,
		ClickCount: 1,
	}).Call(page)
	time.Sleep(40*time.Millisecond + time.Duration(ri(40))*time.Millisecond)
	err := (proto.InputDispatchMouseEvent{
		Type: proto.InputDispatchMouseEventTypeMouseReleased,
		X:    x, Y: y,
		Button:     proto.InputMouseButtonLeft,
		ClickCount: 1,
	}).Call(page)
	return err == nil
}

func minF(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

func maxF(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}

// fillGrokEmail 确保邮箱输入框出现、填入邮箱并提交。整段最多重试 3 次：某次
// 输入/提交卡住或超时，就重新加载注册页（含重新等待 Cloudflare）再来一遍，而不是
// 直接判失败。保留「真实按键优先」（typeHumanStable 内部先逐字输入、写不进去才
// 用原生 setter 兜底），以维持 Turnstile 评分。
func fillGrokEmail(ctx context.Context, page *rod.Page, in Input) error {
	const signupURL = "https://accounts.x.ai/sign-up?redirect=grok-com&return_to=%2F"
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if attempt > 0 {
			in.logf("邮箱步重试第 %d 次：重新加载注册页", attempt)
			if err := rod.Try(func() {
				nav := page.Timeout(120 * time.Second)
				nav.MustNavigate(signupURL)
				nav.MustWaitLoad()
			}); err != nil {
				lastErr = fmt.Errorf("重新加载注册页失败: %w", err)
				continue
			}
			if err := waitForCloudflare(ctx, page, in, 5*time.Minute); err != nil {
				lastErr = err
				continue
			}
		}
		hasEmail := false
		_ = rod.Try(func() { hasEmail = page.Timeout(5 * time.Second).MustHas("input[type='email']") })
		if !hasEmail {
			if pendingCaptcha(page) {
				if err := waitForCloudflare(ctx, page, in, 10*time.Minute); err != nil {
					lastErr = err
					continue
				}
			}
			clickEmailSignup(page)
		}
		if err := typeHumanStable(page, "input[type='email']", in.Email, 45*time.Second); err != nil {
			lastErr = fmt.Errorf("输入邮箱失败: %w", err)
			continue
		}
		if err := clickSubmit(page, in); err != nil {
			lastErr = fmt.Errorf("点击邮箱提交按钮失败: %w", err)
			continue
		}
		return nil
	}
	return lastErr
}

func fillProfileHuman(page *rod.Page, in Input) error {
	// Prefer rod keyboard input over JS value setter — managed Turnstile scores this higher.
	type field struct {
		sel string
		val string
	}
	// Try labeled placeholders first, then generic visible inputs.
	candidates := []field{
		{`input[name="firstName"], input[name="first_name"], input[placeholder*="First" i], input[autocomplete="given-name"]`, in.FirstName},
		{`input[name="lastName"], input[name="last_name"], input[placeholder*="Last" i], input[autocomplete="family-name"]`, in.LastName},
		{`input[type="password"]`, in.Password},
	}
	filled := 0
	for _, c := range candidates {
		if err := typeHumanStable(page, c.sel, c.val, 12*time.Second); err != nil {
			continue
		}
		filled++
		time.Sleep(200*time.Millisecond + time.Duration(ri(300))*time.Millisecond)
	}
	if filled < 2 {
		// fallback: first two text + password by visibility order
		_, err := page.Eval(fmt.Sprintf(`() => {
			const visible = el => !!(el.offsetWidth || el.offsetHeight || el.getClientRects().length);
			const setValue = (el, value) => {
				el.focus();
				const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set;
				setter.call(el, value);
				el.dispatchEvent(new InputEvent('input', { bubbles: true, data: value, inputType: 'insertText' }));
				el.dispatchEvent(new Event('change', { bubbles: true }));
			};
			const inputs = [...document.querySelectorAll('input')].filter(visible);
			const textInputs = inputs.filter(i => !['password','email','checkbox','hidden','submit'].includes((i.type||'').toLowerCase()));
			const password = inputs.find(i => (i.type||'').toLowerCase() === 'password');
			if (textInputs[0]) setValue(textInputs[0], %q);
			if (textInputs[1]) setValue(textInputs[1], %q);
			if (password) setValue(password, %q);
			return true;
		}`, in.FirstName, in.LastName, in.Password))
		if err != nil {
			return err
		}
	}
	return nil
}

// typeHumanStable 往输入框写入文本：每次尝试重新定位元素并使用独立超时，避免元素
// 沉淀在等待阶段的绝对 deadline 上（等待+逐字输入共用一个预算时，负载高或
// 输入框重渲染会报 context deadline exceeded）；写入后校验实际值，连续失败后才
// 用原生 setter 兜底（优先真实按键，Turnstile 评分更高）。
func typeHumanStable(page *rod.Page, selector, value string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for attempt := 0; time.Now().Before(deadline); attempt++ {
		lastErr = func() error {
			if attempt < 2 {
				el, err := visibleElement(page, selector, 15*time.Second)
				if err != nil {
					return err
				}
				if err = typeHuman(el, value); err != nil {
					return err
				}
			} else if err := setInputValueJS(page, selector, value); err != nil {
				return err
			}
			if got := readInputValue(page, selector); got != value {
				return fmt.Errorf("写入后内容不符(实际长度 %d)", len(got))
			}
			return nil
		}()
		if lastErr == nil {
			return nil
		}
		time.Sleep(1 * time.Second)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("等待输入框超时: %s", selector)
	}
	return lastErr
}

// visibleElement 在选择器的所有匹配里挑第一个「可见」的：页面可能同时渲染
// 多份表单（一份隐藏），首个匹配不一定可见。
func visibleElement(page *rod.Page, selector string, timeout time.Duration) (*rod.Element, error) {
	deadline := time.Now().Add(timeout)
	for {
		els, err := page.Timeout(6 * time.Second).Elements(selector)
		if err == nil {
			for _, el := range els {
				if v, verr := el.Visible(); verr == nil && v {
					return el.CancelTimeout().Timeout(15 * time.Second), nil
				}
			}
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("等待可见元素超时: %s", selector)
		}
		time.Sleep(500 * time.Millisecond)
	}
}

// setInputValueJS 用原生 setter 赋值并派发 input/change，兼容受控组件。
func setInputValueJS(page *rod.Page, selector, value string) error {
	ok, err := page.Timeout(10*time.Second).Eval(`(selector, value) => {
		const els = [...document.querySelectorAll(selector)];
		const visible = e => !!(e.offsetWidth || e.offsetHeight || e.getClientRects().length);
		const el = els.find(visible) || els[0];
		if (!el) return false;
		el.focus();
		const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value').set;
		setter.call(el, value);
		el.dispatchEvent(new InputEvent('input', { bubbles: true, data: value, inputType: 'insertText' }));
		el.dispatchEvent(new Event('change', { bubbles: true }));
		return true;
	}`, selector, value)
	if err != nil {
		return err
	}
	if !ok.Value.Bool() {
		return fmt.Errorf("未找到输入框: %s", selector)
	}
	return nil
}

func readInputValue(page *rod.Page, selector string) string {
	got, err := page.Timeout(10*time.Second).Eval(`selector => {
		const els = [...document.querySelectorAll(selector)];
		const visible = e => !!(e.offsetWidth || e.offsetHeight || e.getClientRects().length);
		const el = els.find(visible) || els[0];
		return el ? el.value : '';
	}`, selector)
	if err != nil {
		return ""
	}
	return got.Value.Str()
}

func typeHuman(el *rod.Element, text string) error {
	if el == nil {
		return fmt.Errorf("nil element")
	}
	_ = el.ScrollIntoView()
	// 点击聚焦可能因浮层遮挡一直等「可交互」直到超时；限时尝试，失败改用 JS 聚焦
	if err := rod.Try(func() { el.Timeout(5 * time.Second).MustClick() }); err != nil {
		if _, ferr := el.Eval(`() => this.focus()`); ferr != nil {
			return ferr
		}
	}
	// clear existing
	_ = el.SelectAllText()
	_ = el.Input("")
	// insert in small chunks
	for _, r := range text {
		if err := el.Input(string(r)); err != nil {
			return err
		}
		time.Sleep(40*time.Millisecond + time.Duration(ri(70))*time.Millisecond)
	}
	return nil
}

func clickCompleteSignup(page *rod.Page) bool {
	ok, err := page.Eval(`() => {
		const visible = el => !!(el.offsetWidth || el.offsetHeight || el.getClientRects().length);
		const submit = [...document.querySelectorAll('button')].find(b =>
			visible(b) && /Complete|完成注册|Sign up|Create account|创建账户/i.test(b.innerText || '')
		);
		if (!submit) return false;
		submit.click();
		return true;
	}`)
	if err != nil {
		return false
	}
	return ok.Value.Bool()
}

func humanIdle(page *rod.Page, d time.Duration) {
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		x := 200 + float64(ri(800))
		y := 150 + float64(ri(500))
		_ = page.Mouse.MoveTo(proto.NewPoint(x, y))
		time.Sleep(120*time.Millisecond + time.Duration(ri(180))*time.Millisecond)
	}
}

func hasText(page *rod.Page, selector, pattern string) bool {
	_, err := page.ElementR(selector, pattern)
	return err == nil
}

func captureAuth(page *rod.Page, in Input) (map[string]any, error) {
	if !strings.Contains(page.MustInfo().URL, "grok.com") {
		nav := page.Timeout(60 * time.Second)
		nav.MustNavigate("https://grok.com/")
		nav.MustWaitLoad()
	}
	cookies, err := page.Cookies(nil)
	if err != nil {
		return nil, fmt.Errorf("读取 Cookie 失败: %w", err)
	}
	var cookieList []map[string]any
	for _, c := range cookies {
		cookieList = append(cookieList, map[string]any{
			"name":     c.Name,
			"value":    c.Value,
			"domain":   c.Domain,
			"path":     c.Path,
			"expires":  c.Expires,
			"httpOnly": c.HTTPOnly,
			"secure":   c.Secure,
			"sameSite": c.SameSite,
		})
	}

	storageRaw := page.MustEval(`() => JSON.stringify({
		localStorage: Object.fromEntries(Object.entries(localStorage)),
		sessionStorage: Object.fromEntries(Object.entries(sessionStorage)),
		location: location.href
	})`).String()
	var storage map[string]any
	_ = json.Unmarshal([]byte(storageRaw), &storage)

	return map[string]any{
		"auth_mode":   "grok_browser_session",
		"platform":    "grok",
		"email":       in.Email,
		"password":    in.Password,
		"first_name":  in.FirstName,
		"last_name":   in.LastName,
		"captured_at": time.Now().UTC().Format(time.RFC3339),
		"cookies":     cookieList,
		"storage":     storage,
	}, nil
}

func normalizeProxy(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.Contains(raw, "://") {
		return raw
	}
	parts := strings.Split(raw, ":")
	switch len(parts) {
	case 2:
		return "http://" + parts[0] + ":" + parts[1]
	case 4:
		return "http://" + url.QueryEscape(parts[2]) + ":" + url.QueryEscape(parts[3]) + "@" + parts[0] + ":" + parts[1]
	default:
		return "http://" + raw
	}
}

func parseProxy(raw string) (server, user, pass string, err error) {
	u, err := url.Parse(normalizeProxy(raw))
	if err != nil {
		return "", "", "", err
	}
	if u.Host == "" {
		return "", "", "", fmt.Errorf("代理缺少 host: %s", raw)
	}
	scheme := u.Scheme
	if scheme == "" {
		scheme = "http"
	}
	server = scheme + "://" + u.Host
	if u.User != nil {
		user = u.User.Username()
		pass, _ = u.User.Password()
	}
	return server, user, pass, nil
}

func trimText(s string, n int) string {
	s = strings.TrimSpace(strings.Join(strings.Fields(s), " "))
	if len(s) <= n {
		return s
	}
	return s[:n]
}
