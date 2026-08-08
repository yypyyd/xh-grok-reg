package grokreg

import (
	"context"
	cryptorand "crypto/rand"
	"fmt"
	"math/big"
	"strings"
)

const userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/150.0.0.0 Safari/537.36"

type Input struct {
	Email     string
	Password  string
	FirstName string
	LastName  string
	Proxy     string
	Headless  bool

	// TurnstilePython/TurnstileScript/TurnstileMode locate the CloakBrowser mint
	// helper that signs a Cloudflare Turnstile token for x.ai. Empty values fall
	// back to the GROK_TURNSTILE_* environment variables and finally to the
	// server defaults in turnstile_mint.go.
	TurnstilePython string
	TurnstileScript string
	TurnstileMode   string

	// Engine selects the registration path. "protocol" (default) runs the whole
	// flow over HTTP/gRPC and only spawns a browser to mint the Turnstile token;
	// "browser" keeps the legacy rod flow. Empty defaults to protocol.
	Engine string

	// Impersonate / ImpersonateFallback control the TLS fingerprint of the
	// protocol client (default chrome_131, fallback chrome_124,chrome_120).
	Impersonate         string
	ImpersonateFallback string

	// FlareSolverrURL enables a Cloudflare clearance fallback: when the protocol
	// client is blocked, cf_clearance cookies are fetched from FlareSolverr and
	// reused. ClearanceProxy is the egress FlareSolverr uses; ClearanceURLs is a
	// comma-separated list of hosts to prewarm. All optional.
	FlareSolverrURL string
	ClearanceProxy  string
	ClearanceURLs   string

	WaitCode func(ctx context.Context) (string, error)
	Log      func(format string, a ...any)
	SaveShot func(png []byte)

	// mintProxy is the loopback proxy Chromium uses for this registration. The
	// mint runs through the same egress so the token's remote IP matches the
	// submission. Set internally by registerBrowser.
	mintProxy string
}

type Result struct {
	AuthJSON map[string]any `json:"auth_json"`
}

func (in Input) logf(format string, a ...any) {
	if in.Log != nil {
		in.Log(format, a...)
	}
}

func Register(ctx context.Context, in Input) (*Result, error) {
	if in.WaitCode == nil {
		return nil, fmt.Errorf("缺少验证码回调")
	}
	if in.Email == "" {
		return nil, fmt.Errorf("缺少邮箱")
	}
	if in.Password == "" {
		in.Password = GenPassword(16)
	}
	if in.FirstName == "" {
		in.FirstName = firstNames[ri(len(firstNames))]
	}
	if in.LastName == "" {
		in.LastName = lastNames[ri(len(lastNames))]
	}
	if strings.EqualFold(strings.TrimSpace(in.Engine), "browser") {
		return registerBrowser(ctx, in)
	}
	return registerProtocol(ctx, in)
}

var firstNames = []string{"Alex", "Jamie", "Taylor", "Jordan", "Casey", "Morgan", "Riley", "Avery", "Quinn", "Parker", "Cameron", "Reese"}
var lastNames = []string{"Ray", "Lee", "Cole", "Reed", "Hunt", "Ford", "Shaw", "Gray", "Vance", "Brooks", "Hayes", "Sloan"}

func ri(n int) int {
	if n <= 0 {
		return 0
	}
	v, err := cryptorand.Int(cryptorand.Reader, big.NewInt(int64(n)))
	if err != nil {
		return 0
	}
	return int(v.Int64())
}

func GenPassword(n int) string {
	const lower = "abcdefghijkmnpqrstuvwxyz"
	const upper = "ABCDEFGHJKLMNPQRSTUVWXYZ"
	const digit = "23456789"
	all := lower + upper + digit
	if n < 12 {
		n = 12
	}
	b := make([]byte, n)
	b[0] = upper[ri(len(upper))]
	b[1] = lower[ri(len(lower))]
	b[2] = digit[ri(len(digit))]
	for i := 3; i < n; i++ {
		b[i] = all[ri(len(all))]
	}
	return string(b)
}
