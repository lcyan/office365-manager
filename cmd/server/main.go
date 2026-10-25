// office365-manager 服务器入口：装配配置、认证、Graph 客户端与 HTTP 路由。
package main

import (
	"flag"
	"log/slog"
	"net/http"
	"os"

	"github.com/lcyan/office365-manager/internal/config"
)

func main() {
	addr := flag.String("addr", ":5000", "HTTP 监听地址")
	configFile := flag.String("config", envOr("CONFIG_FILE", "config.json"), "配置文件路径")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	store, err := config.Load(*configFile, "") // defaultHash 由 auth 包提供，M2 接入
	if err != nil {
		logger.Error("加载配置失败", "err", err)
		os.Exit(1)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	logger.Info("启动服务", "addr", *addr, "config", *configFile, "organizations", len(store.Orgs()))
	if err := http.ListenAndServe(*addr, mux); err != nil {
		logger.Error("服务退出", "err", err)
		os.Exit(1)
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
