package proxyutil

import (
	cryptorand "crypto/rand"
	"encoding/hex"
	"net/url"
	"strconv"
	"strings"
)

// WithBestGoTaskSession gives a dynamic BestGo residential proxy a stable
// task-level session. Without it, BestGo rotates the exit per request, which
// makes one browser/protocol registration appear from multiple IP addresses.
// An explicit user-provided session is preserved.
func WithBestGoTaskSession(raw string) string {
	raw = strings.TrimSpace(raw)
	lower := strings.ToLower(raw)
	if !strings.Contains(raw, "://") ||
		(!strings.Contains(lower, "bestgo") && !strings.Contains(lower, "zone-custom")) {
		return raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.User == nil {
		return raw
	}
	user := u.User.Username()
	if strings.Contains(strings.ToLower(user), "-session-") {
		return raw
	}
	pass, hasPass := u.User.Password()
	user += "-session-" + sessionToken(8)
	if hasPass {
		u.User = url.UserPassword(user, pass)
	} else {
		u.User = url.User(user)
	}
	return u.String()
}

func sessionToken(n int) string {
	b := make([]byte, n)
	if _, err := cryptorand.Read(b); err != nil {
		return "fallback" + strconv.Itoa(n)
	}
	return hex.EncodeToString(b)
}
