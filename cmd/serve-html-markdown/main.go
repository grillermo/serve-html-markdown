package main

import (
	"fmt"
	"io"
	"log/slog"
	"os"

	"serve-html-markdown/internal/config"
)

const usage = `usage: serve-html-markdown [command]

commands:
  serve               run the HTTP server and job workers (default)
  migrate             apply database migrations
  seed-admin          create the admin user from ADMIN_EMAIL / ADMIN_PASSWORD
  youtube-reauth-url  print the YouTube re-authorization URL
  sync-files          sync the files/ directory into the database
  help                show this help
`

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, nil)))
	os.Exit(run(os.Args[1:]))
}

// run executes the subcommand named by args[0] (default "serve") and returns
// the process exit code.
func run(args []string) int {
	return runTo(args, os.Stdout, os.Stderr)
}

func runTo(args []string, stdout, stderr io.Writer) int {
	cmd := "serve"
	if len(args) > 0 {
		cmd = args[0]
	}

	switch cmd {
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage)
		return 0
	case "serve":
		root, err := os.Getwd()
		if err != nil {
			slog.Error("getwd", "err", err)
			return 1
		}
		cfg := config.Load(root)
		slog.Info("serve: not implemented yet", "port", cfg.Port, "env", cfg.AppEnv)
		return 0
	case "migrate", "seed-admin":
		root, err := os.Getwd()
		if err != nil {
			fmt.Fprintf(stderr, "%s: getwd: %v\n", cmd, err)
			return 1
		}
		cfg := config.Load(root)
		if cmd == "migrate" {
			return migrate(cfg, stdout, stderr)
		}
		return seedAdmin(cfg, stdout, stderr)
	case "youtube-reauth-url", "sync-files":
		fmt.Fprintf(stderr, "%s: not implemented\n", cmd)
		return 1
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n%s", cmd, usage)
		return 2
	}
}
