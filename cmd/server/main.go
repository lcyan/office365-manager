// office365-manager 服务器入口：装配配置、会话与 HTTP 路由。
package main

import (
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/lcyan/office365-manager/internal/auth"
	"github.com/lcyan/office365-manager/internal/config"
	"github.com/lcyan/office365-manager/internal/web"
)

func main() {
	addr := flag.String("addr", envOr("ADDR", ":5000"), "HTTP 监听地址")
	configFile := flag.String("config", envOr("CONFIG_FILE", "config.json"), "配置文件路径")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	secret, err := sessionSecret()
	if err != nil {
		logger.Error("启动失败", "err", err)
		os.Exit(1)
	}

	defaultHash, err := auth.HashPassword(auth.DefaultPassword)
	if err != nil {
		logger.Error("生成默认密码哈希失败", "err", err)
		os.Exit(1)
	}
	store, err := config.Load(*configFile, defaultHash)
	if err != nil {
		logger.Error("加载配置失败", "err", err)
		os.Exit(1)
	}

	sessions := web.NewSessionManager([]byte(secret), os.Getenv("COOKIE_SECURE") == "1")

	server, err := web.New(web.Deps{
		Store:    store,
		Sessions: sessions,
		Limiter:  auth.NewLimiter(5, 30*time.Second),
		Log:      logger,
	})
	if err != nil {
		logger.Error("初始化服务失败", "err", err)
		os.Exit(1)
	}

	logger.Info("启动服务", "addr", *addr, "config", *configFile)
	srv := &http.Server{
		Addr:              *addr,
		Handler:           server.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	if err := srv.ListenAndServe(); err != nil {
		logger.Error("服务退出", "err", err)
		os.Exit(1)
	}
}

// sessionSecret 返回会话签名密钥：优先 SESSION_SECRET；
// DEV_MODE=1 时生成进程内临时密钥；否则拒绝启动（避免可伪造会话）。
func sessionSecret() (string, error) {
	if secret := os.Getenv("SESSION_SECRET"); secret != "" {
		return secret, nil
	}
	if os.Getenv("DEV_MODE") == "1" {
		buf := make([]byte, 32)
		if _, err := rand.Read(buf); err != nil {
			return "", fmt.Errorf("生成开发密钥失败：%w", err)
		}
		slog.Warn("DEV_MODE=1：使用进程内临时会话密钥，重启后需重新登录；切勿用于生产。")
		return hex.EncodeToString(buf), nil
	}
	return "", fmt.Errorf("未设置 SESSION_SECRET。请通过环境变量提供足够长的随机字符串（生产必须），或设置 DEV_MODE=1 用于本地开发")
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
