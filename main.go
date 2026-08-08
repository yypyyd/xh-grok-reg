package main

import (
	"embed"
	"io/fs"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"xh-grok-reg/internal/auth"
	"xh-grok-reg/internal/browserboot"
	"xh-grok-reg/internal/db"
	"xh-grok-reg/internal/handlers"

	"github.com/gin-gonic/gin"
)

//go:embed static
var staticFS embed.FS

func main() {
	database, err := db.Init("grok-register.db")
	if err != nil {
		log.Fatalf("init db: %v", err)
	}
	if err := database.Exec(
		"UPDATE grok_registrations SET status = 'register_failed', log = log || ? WHERE status IN ('registering', 'waiting_code')",
		"\n"+time.Now().Format("2006-01-02 15:04:05")+" ✗ 程序重启，Grok 任务中断，判定为失败",
	).Error; err != nil {
		log.Printf("reset grok registering on boot: %v", err)
	}

	authSvc, err := auth.New(database)
	if err != nil {
		log.Fatalf("init auth: %v", err)
	}
	browser := browserboot.New()
	browser.EnsureAsync()

	gin.SetMode(gin.ReleaseMode)
	r := gin.Default()
	h, err := handlers.New(database, authSvc, browser)
	if err != nil {
		log.Fatalf("init handlers: %v", err)
	}
	defer h.MailboxVerifier.Stop()

	r.GET("/healthz", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })
	r.POST("/api/login", h.Login)
	api := r.Group("/api", h.AuthRequired())
	{
		api.POST("/change-password", h.ChangePassword)
		api.GET("/stats", h.Stats)
		api.GET("/browser/status", h.BrowserStatus)
		api.GET("/mailboxes", h.MailboxList)
		api.DELETE("/mailboxes", h.MailboxDeleteAll)
		api.POST("/mailboxes", h.MailboxCreate)
		api.POST("/mailboxes/import", h.MailboxImport)
		api.POST("/mailboxes/reauthenticate", h.MailboxReauthenticate)
		api.POST("/mailboxes/:id/verify", h.MailboxVerify)
		api.PUT("/mailboxes/:id", h.MailboxUpdate)
		api.DELETE("/mailboxes/:id", h.MailboxDelete)
		api.GET("/mailboxes/:id/messages", h.MailboxMessages)
		api.GET("/mailboxes/:id/message", h.MailboxMessage)
		api.GET("/settings", h.SettingsGet)
		api.PUT("/settings", h.SettingsSave)
		api.POST("/proxy/test", h.ProxyTest)
		api.GET("/grok/registrations", h.GrokList)
		api.DELETE("/grok/registrations", h.GrokDeleteAll)
		api.POST("/grok/registrations", h.GrokStart)
		api.POST("/grok/produce", h.GrokProduce)
		api.GET("/grok/produce/status", h.GrokProduceStatus)
		api.POST("/grok/produce/stop", h.GrokProduceStop)
		api.POST("/grok/registrations/:id/code", h.GrokSubmitCode)
		api.POST("/grok/registrations/:id/stop", h.GrokStop)
		api.DELETE("/grok/registrations/:id", h.GrokDelete)
		api.GET("/grok/registrations/:id/logs", h.GrokLog)
		api.GET("/grok/registrations/:id/shot", h.GrokShot)
		api.POST("/grok/download", h.GrokDownload)
		api.POST("/grok/registrations/livecheck", h.GrokLiveCheckStart)
		api.GET("/grok/registrations/livecheck/status", h.GrokLiveCheckStatus)
		api.POST("/grok/registrations/:id/livecheck", h.GrokLiveCheckOne)
	}

	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		log.Fatalf("static fs: %v", err)
	}
	assetsSub, err := fs.Sub(sub, "assets")
	if err != nil {
		log.Fatalf("static assets fs: %v", err)
	}
	r.StaticFS("/assets", http.FS(assetsSub))
	indexHTML, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		log.Fatalf("read frontend entry: %v", err)
	}
	serveApp := func(c *gin.Context) { c.Data(http.StatusOK, "text/html; charset=utf-8", indexHTML) }
	r.GET("/", serveApp)
	r.NoRoute(func(c *gin.Context) {
		if strings.HasPrefix(c.Request.URL.Path, "/api/") {
			c.JSON(http.StatusNotFound, gin.H{"error": "not found"})
			return
		}
		serveApp(c)
	})

	addr := os.Getenv("ADDR")
	if addr == "" {
		addr = ":9000"
	}
	if !strings.Contains(addr, ":") {
		addr = ":" + addr
	}
	log.Printf("xh-grok-reg listening on http://localhost%s", addr)
	if err := r.Run(addr); err != nil {
		log.Fatal(err)
	}
}
