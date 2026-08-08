package grokreg

import (
	"encoding/json"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/proto"
)

// reuseTurnstile mirrors the reference project's getTurnstileToken routine.
// In particular, it never resizes a hidden managed iframe: doing that turns a
// normal invisible widget into a false "interactive challenge" in our own
// diagnostics. DrissionPage can pierce the widget's closed shadow roots; Rod's
// DOM-domain ShadowRoot/Frame methods provide the same behavior here.
func reuseTurnstile(page *rod.Page, in Input) bool {
	in.logf("页面安全校验 token 尚未签发，按参考项目复用 Turnstile 组件")
	_, _ = page.Eval(`() => {
		try {
			if (window.turnstile && typeof window.turnstile.reset === 'function') {
				window.turnstile.reset();
			}
		} catch (e) {}
	}`)

	// Mirror the reference getTurnstileToken loop: click the real checkbox on
	// every pass (not once), and each pass copy any issued token from the input
	// or turnstile.getResponse() back into cf-turnstile-response so the form can
	// read it. x.ai's managed widget issues a token to a genuine checkbox click
	// without any third-party solver.
	deadline := time.Now().Add(20 * time.Second)
	clickedAny := false
	for time.Now().Before(deadline) {
		if syncTurnstileToken(page.Timeout(3*time.Second)) >= 20 {
			in.logf("Turnstile 已签发 token")
			return true
		}
		if tryReuseTurnstileClick(page.Timeout(3 * time.Second)) {
			clickedAny = true
		}
		time.Sleep(time.Second)
	}
	if clickedAny {
		in.logf("Turnstile 组件已自动点击，继续等待 token")
	}
	return false
}

// syncTurnstileToken reads the Turnstile token from the response input or from
// turnstile.getResponse() and writes it back into the cf-turnstile-response
// field, mirroring the reference project's token sync. Returns the token length.
func syncTurnstileToken(page *rod.Page) int {
	v, err := page.Eval(`() => {
		const input = document.querySelector('input[name="cf-turnstile-response"], textarea[name="cf-turnstile-response"]');
		let token = String((input && input.value) || '').trim();
		if (token.length < 20) {
			try {
				if (window.turnstile && typeof window.turnstile.getResponse === 'function') {
					token = String(window.turnstile.getResponse() || '').trim();
				}
			} catch (e) {}
		}
		if (token.length >= 20 && input) {
			const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')?.set
				|| Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value')?.set;
			if (setter) setter.call(input, token); else input.value = token;
			input.dispatchEvent(new Event('input', { bubbles: true }));
			input.dispatchEvent(new Event('change', { bubbles: true }));
		}
		return token.length;
	}`)
	if err != nil {
		return 0
	}
	return int(v.Value.Num())
}

// Turnstile replaces its closed-shadow iframe while it is resetting. Rod may
// therefore observe a valid shadow root followed immediately by a detached
// frame. Keep that transient race inside one guarded attempt instead of
// allowing a stale DOM object to abort the whole registration.
func tryReuseTurnstileClick(page *rod.Page) (clicked bool) {
	defer func() {
		if recover() != nil {
			clicked = false
		}
	}()
	response, err := page.Element(`[name="cf-turnstile-response"]`)
	if err != nil || response == nil {
		return tryReuseTurnstileFallbackClick(page)
	}
	// Mirror the reference getTurnstileToken exactly: the widget mounts in the
	// wrapper's shadow root, and the real checkbox is an <input> inside the
	// cross-origin iframe body's own shadow root. Click that element directly.
	// A computed page-coordinate click on the host box turns the invisible
	// managed widget into a stuck interactive challenge that never issues a
	// token, so coordinate clicking is only a last-resort fallback.
	wrapper, err := response.Parent()
	if err != nil || wrapper == nil {
		return tryReuseTurnstileFallbackClick(page)
	}
	shadow, err := wrapper.ShadowRoot()
	if err != nil || shadow == nil || shadow.Page() == nil {
		return tryReuseTurnstileFallbackClick(page)
	}
	iframe, err := shadow.Element("iframe")
	if err != nil || iframe == nil || iframe.Page() == nil {
		return tryReuseTurnstileFallbackClick(page)
	}
	frame, err := iframe.Frame()
	if err != nil || frame == nil || frame.FrameID == "" {
		return tryReuseTurnstileFallbackClick(page)
	}
	// Spoof screenX/screenY so the synthetic click looks like a real cursor,
	// matching the reference project's iframe.run_js injection.
	_, _ = frame.Eval(`() => {
		try {
			const sx = 800 + Math.floor(Math.random() * 400);
			const sy = 400 + Math.floor(Math.random() * 300);
			Object.defineProperty(MouseEvent.prototype, 'screenX', { configurable: true, get: () => sx });
			Object.defineProperty(MouseEvent.prototype, 'screenY', { configurable: true, get: () => sy });
		} catch (e) {}
	}`)
	body, err := frame.Element("body")
	if err != nil || body == nil || body.Page() == nil {
		return tryReuseTurnstileFallbackClick(page)
	}
	root := body
	if inner, innerErr := body.ShadowRoot(); innerErr == nil && inner != nil && inner.Page() != nil {
		root = inner
	}
	button, err := root.Element(`input, [role="checkbox"], label, button`)
	if err != nil || button == nil || button.Page() == nil {
		return tryReuseTurnstileFallbackClick(page)
	}
	if err := button.Click(proto.InputMouseButtonLeft, 1); err == nil {
		return true
	}
	if mouseClickElement(button) {
		return true
	}
	return tryReuseTurnstileFallbackClick(page)
}

// tryReuseTurnstileFallbackClick clicks the widget's visible host box by page
// coordinate. Rod cannot always pierce Cloudflare's closed shadow root; when the
// element-click path is unavailable this hits the 64px host hotspot instead.
func tryReuseTurnstileFallbackClick(page *rod.Page) bool {
	point, err := page.Eval(`() => {
		const response = document.querySelector('[name="cf-turnstile-response"]');
		const host = response && (response.parentElement || {}).parentElement;
		if (!host) return null;
		const style = getComputedStyle(host);
		const r = host.getBoundingClientRect();
		if (style.display === 'none' || style.visibility === 'hidden' || Number(style.opacity || 1) === 0) return null;
		if (r.width < 100 || r.height < 40) return null;
		return { x: r.left + 21, y: r.top + Math.min(35, r.height / 2), w: r.width, h: r.height };
	}`)
	if err != nil || point == nil {
		return false
	}
	raw, _ := json.Marshal(point.Value)
	var p struct {
		X float64 `json:"x"`
		Y float64 `json:"y"`
		W float64 `json:"w"`
		H float64 `json:"h"`
	}
	if json.Unmarshal(raw, &p) != nil || p.W < 100 || p.H < 40 {
		return false
	}
	return mouseClickAt(page, p.X, p.Y)
}
