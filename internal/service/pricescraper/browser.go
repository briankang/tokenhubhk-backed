package pricescraper

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/launcher"
	"github.com/go-rod/rod/lib/proto"
	"go.uber.org/zap"

	"tokenhub-server/internal/pkg/logger"
)

// =====================================================
// 浏览器管理器
// 管理 headless Chromium 实例，供价格爬虫使用
// 浏览器以独立进程运行，每次爬取使用临时 user-data-dir
// =====================================================

// htmlCacheEntry 页面 HTML 缓存条目
type htmlCacheEntry struct {
	html      string
	fetchedAt time.Time
}

// BrowserManager 管理 headless Chromium 实例
type BrowserManager struct {
	mu        sync.Mutex
	browser   *rod.Browser
	htmlCache map[string]*htmlCacheEntry // URL → 缓存的 HTML（30分钟有效）
	fetchMu   sync.Mutex                // 防止同一页面并发加载
}

// NewBrowserManager 创建浏览器管理器（懒加载，首次使用时启动浏览器）
func NewBrowserManager() *BrowserManager {
	return &BrowserManager{
		htmlCache: make(map[string]*htmlCacheEntry),
	}
}

// ensureBrowser 确保浏览器已启动（懒加载）
func (m *BrowserManager) ensureBrowser() (*rod.Browser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.browser != nil {
		return m.browser, nil
	}

	log := logger.L
	if log == nil {
		log = zap.NewNop()
	}

	// 创建临时用户数据目录，防止数据共享
	tmpDir, err := os.MkdirTemp("", "rod-browser-*")
	if err != nil {
		return nil, fmt.Errorf("创建临时用户目录失败: %w", err)
	}

	// 构建 launcher，支持通过环境变量指定 Chromium 路径
	l := launcher.New().
		Headless(true).
		NoSandbox(true).
		UserDataDir(tmpDir).
		Set("disable-gpu").
		Set("disable-dev-shm-usage").
		Set("no-first-run").
		Set("disable-default-apps")

	// 支持 ROD_BROWSER_BIN 环境变量（Docker 场景）
	if binPath := os.Getenv("ROD_BROWSER_BIN"); binPath != "" {
		l = l.Bin(binPath)
		log.Info("使用指定 Chromium 路径", zap.String("bin", binPath))
	}

	controlURL, err := l.Launch()
	if err != nil {
		return nil, fmt.Errorf("启动 Chromium 失败: %w", err)
	}

	browser := rod.New().ControlURL(controlURL)
	if err := browser.Connect(); err != nil {
		return nil, fmt.Errorf("连接 Chromium 失败: %w", err)
	}

	m.browser = browser
	log.Info("Chromium 浏览器已启动", zap.String("control_url", controlURL))
	return m.browser, nil
}

// FetchRenderedHTML 打开页面并获取 JS 渲染后的完整 HTML
// 支持 30 分钟缓存，避免重复加载同一页面；使用 fetchMu 防止并发加载
func (m *BrowserManager) FetchRenderedHTML(ctx context.Context, url string) (string, error) {
	log := logger.L
	if log == nil {
		log = zap.NewNop()
	}

	// 检查缓存（30分钟有效）
	m.mu.Lock()
	if entry, ok := m.htmlCache[url]; ok && time.Since(entry.fetchedAt) < 30*time.Minute {
		m.mu.Unlock()
		log.Info("使用缓存的页面 HTML",
			zap.String("url", url),
			zap.Int("html_length", len(entry.html)),
			zap.Duration("cache_age", time.Since(entry.fetchedAt)))
		return entry.html, nil
	}
	m.mu.Unlock()

	// 防止并发加载同一页面（第二个请求等待第一个完成后使用缓存）
	m.fetchMu.Lock()
	defer m.fetchMu.Unlock()

	// 二次检查缓存（可能在等锁期间被其他请求填充）
	m.mu.Lock()
	if entry, ok := m.htmlCache[url]; ok && time.Since(entry.fetchedAt) < 30*time.Minute {
		m.mu.Unlock()
		log.Info("使用缓存的页面 HTML（等待后命中）",
			zap.String("url", url),
			zap.Int("html_length", len(entry.html)))
		return entry.html, nil
	}
	m.mu.Unlock()

	html, err := m.doFetchRenderedHTML(ctx, url)
	if err != nil {
		return "", err
	}

	// 写入缓存
	m.mu.Lock()
	m.htmlCache[url] = &htmlCacheEntry{html: html, fetchedAt: time.Now()}
	m.mu.Unlock()

	return html, nil
}

// doFetchRenderedHTML 实际执行浏览器页面加载
func (m *BrowserManager) doFetchRenderedHTML(ctx context.Context, url string) (string, error) {
	log := logger.L
	if log == nil {
		log = zap.NewNop()
	}

	browser, err := m.ensureBrowser()
	if err != nil {
		return "", err
	}

	// 创建新标签页（about:blank）
	page, err := browser.Page(proto.TargetCreateTarget{URL: ""})
	if err != nil {
		return "", fmt.Errorf("创建标签页失败: %w", err)
	}
	defer page.Close()

	// 将 context 绑定到 page，支持超时和取消
	page = page.Context(ctx)

	log.Info("开始加载页面", zap.String("url", url))

	// 导航到目标页面
	if err := page.Navigate(url); err != nil {
		return "", fmt.Errorf("导航到页面失败: %w", err)
	}

	// 等待页面基本加载完成
	if err := page.WaitLoad(); err != nil {
		return "", fmt.Errorf("等待页面加载失败: %w", err)
	}

	// 等待 DOM 稳定（JS 渲染完成）
	if err := page.WaitStable(2 * time.Second); err != nil {
		log.Warn("等待页面稳定超时，继续获取内容", zap.Error(err))
	}

	// 等待 table 元素出现（最多 30 秒，给动态渲染更多时间）
	tableFound := false
	if el, err := page.Timeout(30 * time.Second).Element("table"); err == nil && el != nil {
		tableFound = true
		log.Info("检测到 table 元素，页面渲染完成")
	} else {
		log.Warn("未检测到 table 元素，尝试获取当前页面内容")
	}

	// 获取完整渲染后的 HTML
	html, err := page.HTML()
	if err != nil {
		return "", fmt.Errorf("获取页面 HTML 失败: %w", err)
	}

	// 基本验证
	if !tableFound && !strings.Contains(html, "<table") {
		log.Warn("页面 HTML 中未发现 table 标签",
			zap.String("url", url),
			zap.Int("html_length", len(html)))
	}

	log.Info("页面 HTML 获取成功",
		zap.String("url", url),
		zap.Int("html_length", len(html)),
		zap.Bool("has_table", strings.Contains(html, "<table")))

	return html, nil
}

// Close 关闭浏览器进程并清理资源
func (m *BrowserManager) Close() {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.browser != nil {
		_ = m.browser.Close()
		m.browser = nil

		log := logger.L
		if log == nil {
			log = zap.NewNop()
		}
		log.Info("Chromium 浏览器已关闭")
	}
}
