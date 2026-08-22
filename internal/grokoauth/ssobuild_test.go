package grokoauth

import (
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"
)

func TestSSOFromAuthFallbacks(t *testing.T) {
	if got := SSOFromAuth(map[string]any{"sso": " direct "}); got != "direct" {
		t.Fatalf("direct sso=%q", got)
	}
	auth := map[string]any{"cookies": []any{map[string]any{"name": "sso", "value": " cookie "}}}
	if got := SSOFromAuth(auth); got != "cookie" {
		t.Fatalf("cookie sso=%q", got)
	}
}

func TestStoredCredentialsRoundTrip(t *testing.T) {
	info := &TokenInfo{
		AccessToken: "access", RefreshToken: "refresh", IDToken: "id",
		TokenType: "Bearer", Scope: "openid", ExpiresAt: time.Now().Add(time.Hour).Unix(),
		Email: "user@example.com", Subject: "subject", TeamID: "team",
	}
	stored, ok := FromStored(Credentials(info, "fallback@example.com"))
	if !ok || stored.AccessToken != info.AccessToken || stored.RefreshToken != info.RefreshToken ||
		stored.Email != info.Email || stored.Subject != info.Subject || stored.TeamID != info.TeamID || stored.ExpiresAt != info.ExpiresAt {
		t.Fatalf("round trip mismatch: ok=%v stored=%+v", ok, stored)
	}
}

func TestApplyClaims(t *testing.T) {
	claims, _ := json.Marshal(map[string]any{"email": "claim@example.com", "sub": "sub-1", "team_id": "team-1"})
	token := "header." + base64.RawURLEncoding.EncodeToString(claims) + ".signature"
	info := &TokenInfo{}
	applyClaims(info, token)
	if info.Email != "claim@example.com" || info.Subject != "sub-1" || info.TeamID != "team-1" {
		t.Fatalf("claims not applied: %+v", info)
	}
}

func TestTrustedURL(t *testing.T) {
	for _, raw := range []string{"https://x.ai/path", "https://auth.x.ai/oauth2/token"} {
		if !trustedURL(raw) {
			t.Fatalf("trusted URL rejected: %s", raw)
		}
	}
	for _, raw := range []string{"http://auth.x.ai/", "https://x.ai.example.com/", "https://user@x.ai/"} {
		if trustedURL(raw) {
			t.Fatalf("untrusted URL accepted: %s", raw)
		}
	}
}
