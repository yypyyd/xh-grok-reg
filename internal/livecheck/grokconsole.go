package livecheck

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Grok Console 探测参数：console.x.ai 用 sso cookie 换一次性 DPoP token，
// 再用 DPoP 证明读 /v1/usage 里的 chat / image / video 额度。
const (
	consoleBaseURL = "https://console.x.ai/v1"
	consoleUA      = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"
)

// ConsoleQuota 是 /v1/usage 返回的单项额度。
type ConsoleQuota struct {
	Kind      string `json:"kind"`
	Limit     int    `json:"limit"`
	Used      int    `json:"used"`
	Remaining int    `json:"remaining"`
}

// ConsoleUsage 是一次 Console 探测结果：三态状态 + 额度明细 + 失败原因。
type ConsoleUsage struct {
	Status string
	Quotas []ConsoleQuota
	Reason string
}

// Summary 把额度压成一行文本，如「chat 20/20 · image 5/5」，供列表展示。
func (u ConsoleUsage) Summary() string {
	parts := make([]string, 0, len(u.Quotas))
	for _, q := range u.Quotas {
		parts = append(parts, fmt.Sprintf("%s %d/%d", q.Kind, q.Remaining, q.Limit))
	}
	return strings.Join(parts, " · ")
}

// ProbeConsole 用 sso token 探测账号在 Grok Console 的可用额度。
//
//	200 且额度可解析                       -> alive
//	401 / 403 且为明确的凭据拒绝           -> dead
//	Cloudflare、429、5xx、超时、网络错误   -> unknown
func ProbeConsole(ctx context.Context, client *http.Client, sso string) ConsoleUsage {
	sso = strings.TrimSpace(sso)
	if sso == "" {
		return ConsoleUsage{Status: StatusUnknown, Reason: "缺少 sso token"}
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return ConsoleUsage{Status: StatusUnknown, Reason: "生成 DPoP 密钥失败"}
	}
	jwk := publicDPoPJWK(&key.PublicKey)
	payload, _ := json.Marshal(map[string]any{"jwk": jwk})

	status, body, err := consoleDo(ctx, client, http.MethodPost, consoleBaseURL+"/dpop/token", payload, sso, "", "")
	if err != nil {
		return ConsoleUsage{Status: StatusUnknown, Reason: "请求 DPoP token 失败"}
	}
	if status != http.StatusOK {
		return ConsoleUsage{Status: consoleStatusFor(status, body), Reason: fmt.Sprintf("DPoP token 返回 %d", status)}
	}
	var token struct {
		AccessToken string `json:"access_token"`
		TokenType   string `json:"token_type"`
	}
	if json.Unmarshal(body, &token) != nil || strings.TrimSpace(token.AccessToken) == "" {
		return ConsoleUsage{Status: StatusUnknown, Reason: "DPoP token 响应无效"}
	}

	proof, err := dpopProof(key, jwk, token.AccessToken, http.MethodGet, consoleBaseURL+"/usage")
	if err != nil {
		return ConsoleUsage{Status: StatusUnknown, Reason: "签名 DPoP proof 失败"}
	}
	status, body, err = consoleDo(ctx, client, http.MethodGet, consoleBaseURL+"/usage", nil, sso, token.AccessToken, proof)
	if err != nil {
		return ConsoleUsage{Status: StatusUnknown, Reason: "请求 usage 失败"}
	}
	if status != http.StatusOK {
		return ConsoleUsage{Status: consoleStatusFor(status, body), Reason: fmt.Sprintf("usage 返回 %d", status)}
	}
	var usage struct {
		Quotas []ConsoleQuota `json:"quotas"`
	}
	if json.Unmarshal(body, &usage) != nil {
		return ConsoleUsage{Status: StatusUnknown, Reason: "usage 响应无法解析"}
	}
	return ConsoleUsage{Status: StatusAlive, Quotas: usage.Quotas}
}

// consoleDo 发一次带浏览器头和 sso cookie 的 Console 请求。
func consoleDo(ctx context.Context, client *http.Client, method, endpoint string, body []byte, sso, accessToken, proof string) (int, []byte, error) {
	var reader io.Reader
	if len(body) > 0 {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")
	req.Header.Set("Cookie", "sso="+sso)
	req.Header.Set("Origin", "https://console.x.ai")
	req.Header.Set("Referer", "https://console.x.ai/")
	req.Header.Set("Sec-Fetch-Dest", "empty")
	req.Header.Set("Sec-Fetch-Mode", "cors")
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	req.Header.Set("User-Agent", consoleUA)
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	if accessToken != "" && proof != "" {
		req.Header.Set("Authorization", "DPoP "+accessToken)
		req.Header.Set("DPoP", proof)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return resp.StatusCode, data, nil
}

// consoleStatusFor 把 Console 的非 2xx 响应映射成三态：只有明确的凭据拒绝才判死。
func consoleStatusFor(status int, body []byte) string {
	if status == http.StatusUnauthorized {
		return StatusDead
	}
	if status == http.StatusForbidden && !isCloudflareBody(body) {
		return StatusDead
	}
	return StatusUnknown
}

// isCloudflareBody 判断 403 是否为 Cloudflare 拦截（出口问题，不能判死账号）。
func isCloudflareBody(body []byte) bool {
	s := strings.ToLower(string(body))
	return strings.Contains(s, "cloudflare") || strings.Contains(s, "cf-ray") ||
		strings.Contains(s, "just a moment") || strings.Contains(s, "attention required")
}

func publicDPoPJWK(pub *ecdsa.PublicKey) map[string]string {
	return map[string]string{
		"kty": "EC", "crv": "P-256",
		"x": base64.RawURLEncoding.EncodeToString(pub.X.FillBytes(make([]byte, 32))),
		"y": base64.RawURLEncoding.EncodeToString(pub.Y.FillBytes(make([]byte, 32))),
	}
}

// dpopProof 生成一次请求用的 DPoP 证明（ES256，头部带公钥 jwk）。
func dpopProof(key *ecdsa.PrivateKey, jwk map[string]string, accessToken, method, endpoint string) (string, error) {
	digest := sha256.Sum256([]byte(accessToken))
	jti := make([]byte, 16)
	if _, err := rand.Read(jti); err != nil {
		return "", err
	}
	claims := jwt.MapClaims{
		"jti": hex.EncodeToString(jti),
		"htm": strings.ToUpper(method),
		"htu": endpoint,
		"iat": time.Now().UTC().Unix(),
		"ath": base64.RawURLEncoding.EncodeToString(digest[:]),
	}
	proof := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
	proof.Header["typ"] = "dpop+jwt"
	proof.Header["jwk"] = jwk
	return proof.SignedString(key)
}
