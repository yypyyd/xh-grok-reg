package grokreg

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/go-rod/rod"
)

// The CloakBrowser mint helper (turnstile_mint.py) renders x.ai's Turnstile
// widget in a patched Chromium and returns a signed token. These defaults match
// the server deployment; override them per registration via the Input fields or
// the GROK_TURNSTILE_* environment variables.
const (
	defaultTurnstilePython   = "/opt/cloakbrowser-venv/bin/python"
	defaultTurnstileScript   = "/usr/local/share/grok-reg/turnstile_mint.py"
	defaultTurnstileMode     = "offscreen"
	fallbackTurnstileSitekey = "0x4AAAAAAAhr9JGVDZbrZOo0"
	turnstileSignURL         = "https://accounts.x.ai/sign-up"
)

// mintTurnstileToken shells out to the CloakBrowser mint helper and returns the
// signed Turnstile token. It routes through the registration's loopback proxy so
// the token's remote IP matches the account being created.
func mintTurnstileToken(ctx context.Context, in Input, sitekey, pageURL string) (string, error) {
	python := firstNonEmpty(in.TurnstilePython, os.Getenv("GROK_TURNSTILE_PYTHON"), defaultTurnstilePython)
	script := firstNonEmpty(in.TurnstileScript, os.Getenv("GROK_TURNSTILE_SCRIPT"), defaultTurnstileScript)
	mode := firstNonEmpty(in.TurnstileMode, os.Getenv("TURNSTILE_MODE"), defaultTurnstileMode)
	if strings.TrimSpace(sitekey) == "" {
		sitekey = fallbackTurnstileSitekey
	}
	if strings.TrimSpace(pageURL) == "" {
		pageURL = turnstileSignURL
	}

	cctx, cancel := context.WithTimeout(ctx, 140*time.Second)
	defer cancel()

	args := []string{
		script,
		"--site-key", sitekey,
		"--url", pageURL,
		"--timeout", "110",
		"--mode", mode,
	}
	if proxy := strings.TrimSpace(in.mintProxy); proxy != "" {
		if !strings.Contains(proxy, "://") {
			proxy = "http://" + proxy
		}
		args = append(args, "--proxy", proxy)
	}

	cmd := exec.CommandContext(cctx, python, args...)
	cmd.Env = append(os.Environ(), "CLOAKBROWSER_SUPPRESS_FONT_WARNING=1")
	if strings.TrimSpace(os.Getenv("HOME")) == "" {
		cmd.Env = append(cmd.Env, "HOME=/root")
	}

	var stdout, stderr strings.Builder
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%v: %s", err, tailText(stderr.String(), 200))
	}

	token := strings.TrimSpace(stdout.String())
	if len(token) < 20 {
		return "", fmt.Errorf("mint 返回的 token 过短(len=%d)", len(token))
	}
	return token, nil
}

// pageSitekey reads the Turnstile sitekey mounted on the current page so the
// mint renders the same widget. Returns "" when none is present.
func pageSitekey(page *rod.Page) string {
	v, err := page.Eval(`() => {
		const el = document.querySelector('[data-sitekey]');
		if (el && el.getAttribute('data-sitekey')) return el.getAttribute('data-sitekey');
		const f = document.querySelector('iframe[src*="turnstile"]');
		if (f) {
			const m = (f.getAttribute('src') || '').match(/[?&]sitekey=([^&]+)/);
			if (m) { try { return decodeURIComponent(m[1]); } catch (e) { return m[1]; } }
		}
		return '';
	}`)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(v.Value.Str())
}

// injectMintedToken delivers a signed token into the page's Turnstile state two
// ways: it replays the token through x.ai's own render callback (captured by the
// turnstilePatch extension via window.__cfSolve) so the React form accepts it,
// and it also writes the token into the cf-turnstile-response field for plain
// integrations. Returns the number of site callbacks fired (>0 means the React
// form's state was updated) and whether the DOM field now holds the token.
func injectMintedToken(page *rod.Page, token string) (callbacks int, fieldSet bool) {
	v, err := page.Eval(`(token) => {
		let input = document.querySelector('input[name="cf-turnstile-response"], textarea[name="cf-turnstile-response"]');
		if (!input) {
			input = document.createElement('input');
			input.type = 'hidden';
			input.name = 'cf-turnstile-response';
			(document.forms[0] || document.body).appendChild(input);
		}
		const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, 'value')?.set
			|| Object.getOwnPropertyDescriptor(HTMLTextAreaElement.prototype, 'value')?.set;
		if (setter) setter.call(input, token); else input.value = token;
		input.dispatchEvent(new Event('input', { bubbles: true }));
		input.dispatchEvent(new Event('change', { bubbles: true }));
		let cbs = 0;
		try { if (typeof window.__cfSolve === 'function') cbs = window.__cfSolve(token); } catch (e) { }
		return JSON.stringify({ cbs: cbs, len: String(input.value || '').length });
	}`, token)
	if err != nil {
		return 0, false
	}
	var r struct {
		Cbs int `json:"cbs"`
		Len int `json:"len"`
	}
	if json.Unmarshal([]byte(v.Value.Str()), &r) != nil {
		return 0, false
	}
	return r.Cbs, r.Len >= 20
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func tailText(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
