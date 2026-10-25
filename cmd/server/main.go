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
	"path/filepath"
	"strings"
	"time"

	"github.com/lcyan/office365-manager/assets"
	"github.com/lcyan/office365-manager/internal/auth"
	"github.com/lcyan/office365-manager/internal/config"
	"github.com/lcyan/office365-manager/internal/graph"
	"github.com/lcyan/office365-manager/internal/licenses"
	"github.com/lcyan/office365-manager/internal/web"
)

func main() {
	addr := flag.String("addr", envOr("ADDR", ":5000"), "HTTP 监听地址")
	configFile := flag.String("config", envOr("CONFIG_FILE", "config.json"), "配置文件路径")
	healthcheck := flag.Bool("healthcheck", false, "探测本实例 /healthz 后退出（供 Docker HEALTHCHECK 使用，镜像内无 shell/curl）")
	flag.Parse()

	if *healthcheck {
		os.Exit(runHealthcheck(*addr))
	}

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

	catalog := loadCatalog(*configFile)

	server, err := web.New(web.Deps{
		Store:    store,
		Sessions: web.NewSessionManager([]byte(secret), os.Getenv("COOKIE_SECURE") == "1"),
		Limiter:  auth.NewLimiter(5, 30*time.Second),
		Log:      logger,
		Graph:    graph.New(logger),
		Catalog:  catalog,
	})
	if err != nil {
		logger.Error("初始化服务失败", "err", err)
		os.Exit(1)
	}

	logger.Info("启动服务", "addr", *addr, "config", *configFile, "organizations", len(store.Orgs()))
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

// loadCatalog 加载 SKU 映射：内嵌基准 + 配置目录下可选的覆盖文件
// sku_product_mapping.json（便于不重建镜像即调整产品名）。
func loadCatalog(configFile string) *licenses.Catalog {
	external, err := os.ReadFile(filepath.Join(filepath.Dir(configFile), "sku_product_mapping.json"))
	if err == nil {
		return licenses.NewCatalog(external)
	}
	return licenses.NewCatalog(assets.SKUMapping)
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

// runHealthcheck 探测本实例的 /healthz；0 = 健康。
func runHealthcheck(addr string) int {
	host := addr
	if strings.HasPrefix(host, ":") {
		host = "127.0.0.1" + host
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get("http://" + host + "/healthz")
	if err != nil {
		fmt.Fprintln(os.Stderr, "healthcheck:", err)
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, "healthcheck: status", resp.StatusCode)
		return 1
	}
	return 0
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
