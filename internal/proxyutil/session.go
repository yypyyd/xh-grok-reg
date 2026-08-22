package proxyutil

import (
	cryptorand "crypto/rand"
	"encoding/hex"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// sessionParamRe matches the BestGo username session fragment. A fresh
// session per registration prevents independent tasks from sharing one exit.
var sessionParamRe = regexp.MustCompile(`(?i)(-session-)[^-]*`)

// WithBestGoTaskSession gives a dynamic BestGo residential proxy a stable
// task-level session. Without it, BestGo rotates the exit per request, which
// makes one browser/protocol registration appear from multiple IP addresses.
// Explicit session fragments are replaced so each registration gets a stable,
// task-local exit instead of sharing one fixed IP across all tasks.
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
	token := sessionToken(8)
	if sessionParamRe.MatchString(user) {
		user = sessionParamRe.ReplaceAllString(user, "${1}"+token)
	} else {
		user += "-session-" + token
	}
	pass, hasPass := u.User.Password()
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
