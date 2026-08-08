package livecheck

import (
	"fmt"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
)

// launchBrowser 启动一个用于测活的浏览器实例。复用 rod 默认已下载的 Chromium
// （程序启动时 browserboot 已确保就绪），禁用自动化特征，测活默认无头。
// 返回的 close 负责关闭浏览器、清理 launcher 进程与临时用户数据目录，调用方 defer 即可。
func launchBrowser(headless bool) (browser *rod.Browser, close func(), err error) {
	l := launcher.New().
		Headless(headless).
		NoSandbox(true).
		Set("disable-dev-shm-usage").
		Append("--disable-blink-features", "AutomationControlled").
		Append("--no-first-run", "").
		Append("--no-default-browser-check", "")

	controlURL, err := l.Launch()
	if err != nil {
		return nil, nil, fmt.Errorf("启动测活浏览器失败: %w", err)
	}
	b := rod.New().ControlURL(controlURL)
	if err = b.Connect(); err != nil {
		l.Kill()
		l.Cleanup()
		return nil, nil, fmt.Errorf("连接测活浏览器失败: %w", err)
	}
	close = func() {
		defer func() { _ = recover() }()
		_ = b.Close()
		l.Kill()
		l.Cleanup()
	}
	return b, close, nil
}
