package main

import (
	"fmt"
	"log/slog"
	"os"

	"serve-html-markdown/internal/config"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, nil)))

	cmd := "serve"
	if len(os.Args) > 1 {
		cmd = os.Args[1]
	}

	switch cmd {
	case "serve":
		root, err := os.Getwd()
		if err != nil {
			slog.Error("getwd", "err", err)
			os.Exit(1)
		}
		cfg := config.Load(root)
		slog.Info("serve: not implemented yet", "port", cfg.Port, "env", cfg.AppEnv)
	case "migrate", "seed-admin", "youtube-reauth-url", "sync-files":
		fmt.Fprintf(os.Stderr, "%s: not implemented\n", cmd)
		os.Exit(1)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q (serve, migrate, seed-admin, youtube-reauth-url, sync-files)\n", cmd)
		os.Exit(2)
	}
}
