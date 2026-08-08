package handlers

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/net/proxy"
)

type proxyTestInput struct {
	Proxy string `json:"proxy"`
}

// normalizeProxy 把 host:port:user:pass 之类的写法转成标准 URL；带 scheme 的原样返回。
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
	case 2: // host:port
		return "http://" + parts[0] + ":" + parts[1]
	case 4: // host:port:user:pass
		return "http://" + url.QueryEscape(parts[2]) + ":" + url.QueryEscape(parts[3]) + "@" + parts[0] + ":" + parts[1]
	default:
		return "http://" + raw
	}
}

// ProxyTest 通过给定代理请求一个 IP 探测服务，返回出口 IP，用于验证代理可用。
func (h *Handler) ProxyTest(c *gin.Context) {
	var in proxyTestInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	pu := normalizeProxy(in.Proxy)
	if pu == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "代理为空"})
		return
	}
	u, err := url.Parse(pu)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"ok": false, "error": "代理格式错误"})
		return
	}

	transport := &http.Transport{}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		transport.Proxy = http.ProxyURL(u)
	case "socks5", "socks5h":
		var auth *proxy.Auth
		if u.User != nil {
			pw, _ := u.User.Password()
			auth = &proxy.Auth{User: u.User.Username(), Password: pw}
		}
		dialer, derr := proxy.SOCKS5("tcp", u.Host, auth, proxy.Direct)
		if derr != nil {
			c.JSON(http.StatusOK, gin.H{"ok": false, "error": derr.Error()})
			return
		}
		if cd, ok := dialer.(proxy.ContextDialer); ok {
			transport.DialContext = cd.DialContext
		} else {
			transport.Dial = dialer.Dial //nolint:staticcheck
		}
	default:
		c.JSON(http.StatusOK, gin.H{"ok": false, "error": "不支持的代理类型: " + u.Scheme})
		return
	}

	client := &http.Client{Transport: transport, Timeout: 12 * time.Second}
	start := time.Now()
	resp, err := client.Get("https://api.ipify.org?format=text")
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"ok": false, "error": err.Error()})
		return
	}
	defer resp.Body.Close()
	buf := make([]byte, 64)
	n, _ := resp.Body.Read(buf)
	ip := strings.TrimSpace(string(buf[:n]))
	c.JSON(http.StatusOK, gin.H{"ok": true, "ip": ip, "ms": time.Since(start).Milliseconds()})
}
