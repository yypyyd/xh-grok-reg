package proxyutil

import (
	"net/url"
	"strings"
	"testing"
)

func TestWithBestGoTaskSession(t *testing.T) {
	raw := "http://user-zone-custom-region-US:pass@proxy.bestgo.example:10000"
	got := WithBestGoTaskSession(raw)
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse result: %v", err)
	}
	if u.Host != "proxy.bestgo.example:10000" {
		t.Fatalf("host changed: %q", u.Host)
	}
	if !strings.HasPrefix(u.User.Username(), "user-zone-custom-region-US-session-") {
		t.Fatalf("session missing from username: %q", u.User.Username())
	}
	if pass, _ := u.User.Password(); pass != "pass" {
		t.Fatalf("password changed: %q", pass)
	}
}

func TestWithBestGoTaskSessionReplacesFixedSession(t *testing.T) {
	raw := "http://user-zone-custom-session-fixed-sessTime-5:pass@proxy.bestgo.example:10000"
	got := WithBestGoTaskSession(raw)
	u, err := url.Parse(got)
	if err != nil {
		t.Fatalf("parse result: %v", err)
	}
	user := u.User.Username()
	if strings.Contains(user, "-session-fixed") || !strings.Contains(user, "-sessTime-5") {
		t.Fatalf("session replacement lost data: %q", user)
	}
	if got == WithBestGoTaskSession(raw) {
		t.Fatalf("session not randomized per call: %q", got)
	}
}

func TestWithBestGoTaskSessionLeavesOtherProxyAlone(t *testing.T) {
	raw := "http://user:pass@proxy.example:8080"
	if got := WithBestGoTaskSession(raw); got != raw {
		t.Fatalf("ordinary proxy changed: %q", got)
	}
}
