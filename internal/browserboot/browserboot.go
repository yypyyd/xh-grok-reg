// Package browserboot 负责在程序启动时确保 rod 所需的 Chromium 浏览器已就绪，
// 未就绪则自动下载，并对外暴露下载进度供仪表盘展示。
package browserboot

import (
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/go-rod/rod/lib/launcher"
)

// Status 浏览器就绪 / 下载状态快照。
type Status struct {
	Ready       bool   `json:"ready"`       // 浏览器是否已就绪（可以生产）
	Downloading bool   `json:"downloading"` // 是否正在下载
	Percent     int    `json:"percent"`     // 当前阶段进度 0-100
	Phase       string `json:"phase"`       // checking / downloading / unzip / ready / error
	Message     string `json:"message"`     // 面向用户的提示
	Error       string `json:"error"`       // 失败原因
}

// Manager 管理浏览器下载状态，实现 rod launcher 的 utils.Logger 接口以捕获下载进度。
type Manager struct {
	mu   sync.RWMutex
	st   Status
	once sync.Once
}

func New() *Manager {
	return &Manager{st: Status{Phase: "checking", Message: "正在检查浏览器..."}}
}

// Snapshot 返回状态副本。
func (m *Manager) Snapshot() Status {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.st
}

// Ready 浏览器是否已就绪。
func (m *Manager) Ready() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.st.Ready
}

func (m *Manager) set(f func(*Status)) {
	m.mu.Lock()
	f(&m.st)
	m.mu.Unlock()
}

// Println 实现 launcher.Browser.Logger（utils.Logger）接口，解析 fetchup 的进度事件。
// 事件形如：("Download:", url) / ("Progress:", "50%") / ("Unzip:", dir) / ("Downloaded:", to)。
func (m *Manager) Println(vs ...interface{}) {
	if len(vs) == 0 {
		return
	}
	tag := strings.TrimSpace(fmt.Sprint(vs[0]))
	switch {
	case strings.HasPrefix(tag, "Progress:"):
		if len(vs) > 1 {
			ps := strings.TrimSuffix(strings.TrimSpace(fmt.Sprint(vs[1])), "%")
			if n, err := strconv.Atoi(ps); err == nil {
				m.set(func(s *Status) {
					s.Downloading = true
					if s.Phase != "unzip" {
						s.Phase = "downloading"
					}
					s.Percent = n
					if s.Phase == "unzip" {
						s.Message = fmt.Sprintf("正在解压浏览器 %d%%", n)
					} else {
						s.Message = fmt.Sprintf("正在下载浏览器 %d%%", n)
					}
				})
			}
		}
	case strings.HasPrefix(tag, "Download:"):
		m.set(func(s *Status) {
			s.Downloading = true
			s.Phase = "downloading"
			s.Percent = 0
			s.Message = "开始下载浏览器..."
		})
	case strings.HasPrefix(tag, "Unzip:"):
		m.set(func(s *Status) {
			s.Downloading = true
			s.Phase = "unzip"
			s.Percent = 0
			s.Message = "正在解压浏览器..."
		})
	case strings.HasPrefix(tag, "Downloaded:"):
		m.set(func(s *Status) {
			s.Percent = 100
			s.Message = "下载完成，正在校验..."
		})
	}
}

// EnsureAsync 后台确保浏览器就绪（幂等，仅执行一次）。
func (m *Manager) EnsureAsync() {
	m.once.Do(func() { go m.ensure() })
}

func (m *Manager) ensure() {
	b := launcher.NewBrowser()
	b.Logger = m

	// 已存在且可用 → 直接就绪，不下载。
	if err := b.Validate(); err == nil {
		m.set(func(s *Status) {
			*s = Status{Ready: true, Percent: 100, Phase: "ready", Message: "浏览器已就绪"}
		})
		return
	}

	m.set(func(s *Status) {
		s.Ready = false
		s.Downloading = true
		s.Phase = "downloading"
		s.Message = "缺少浏览器，正在下载..."
	})

	if _, err := b.Get(); err != nil {
		m.set(func(s *Status) {
			s.Ready = false
			s.Downloading = false
			s.Phase = "error"
			s.Error = err.Error()
			s.Message = "浏览器下载失败，请检查网络后重启程序"
		})
		return
	}

	m.set(func(s *Status) {
		*s = Status{Ready: true, Percent: 100, Phase: "ready", Message: "浏览器已就绪"}
	})
}
