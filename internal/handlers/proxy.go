package handlers

import (
	"net/http"
	"strings"
	"time"

	"xh-grok-reg/internal/proxyutil"

	"github.com/gin-gonic/gin"
)

type proxyTestInput struct {
	Proxy string `json:"proxy"`
}

// ProxyTest 通过给定代理请求一个 IP 探测服务，返回出口 IP，用于验证代理可用。
func (h *Handler) ProxyTest(c *gin.Context) {
	var in proxyTestInput
	if err := c.ShouldBindJSON(&in); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	if strings.TrimSpace(in.Proxy) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "代理为空"})
		return
	}
	transport, err := proxyutil.Transport(in.Proxy)
	if err != nil {
		c.JSON(http.StatusOK, gin.H{"ok": false, "error": err.Error()})
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
