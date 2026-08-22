package proxyutil

import (
	"net/http"
	"testing"
)

func TestNormalize(t *testing.T) {
	tests := map[string]string{
		"":                       "",
		"proxy.example:8080":     "http://proxy.example:8080",
		"proxy.example:8080:u:p": "http://u:p@proxy.example:8080",
		"socks5://proxy:1080":    "socks5://proxy:1080",
	}
	for input, want := range tests {
		if got := Normalize(input); got != want {
			t.Errorf("Normalize(%q)=%q, want %q", input, got, want)
		}
	}
}

func TestHTTPTransport(t *testing.T) {
	transport, err := Transport("proxy.example:8080:user:pass")
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest(http.MethodGet, "https://example.com", nil)
	proxyURL, err := transport.Proxy(req)
	if err != nil {
		t.Fatal(err)
	}
	if proxyURL.Host != "proxy.example:8080" || proxyURL.User.Username() != "user" {
		t.Fatalf("unexpected proxy URL: %s", proxyURL)
	}
	if _, err = Transport("ftp://proxy.example:21"); err == nil {
		t.Fatal("unsupported proxy scheme accepted")
	}
}
