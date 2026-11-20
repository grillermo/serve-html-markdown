package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"serve-html-markdown/internal/config"
	"serve-html-markdown/internal/db"
	"serve-html-markdown/internal/expansion"
	"serve-html-markdown/internal/files"
	"serve-html-markdown/internal/jobs"
	"serve-html-markdown/internal/llm"
	"serve-html-markdown/internal/store"
	"serve-html-markdown/internal/video"
	"serve-html-markdown/internal/web"
)

// shutdownGrace is how long in-flight requests get after SIGTERM; the job
// runner then gets its own jobs.DefaultShutdownGrace.
const shutdownGrace = 30 * time.Second

// serve runs the HTTP server and, unless --no-jobs is given, the job workers,
// until SIGINT or SIGTERM. The shadow run of the parity check passes --no-jobs
// so a second process never claims jobs of the production queue.
func serve(cfg config.Config, args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(stderr)
	noJobs := fs.Bool("no-jobs", false, "do not run the background job workers")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "serve: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	if cfg.SecretKeyBase == "" {
		fmt.Fprintln(stderr, "serve: SECRET_KEY_BASE is required to sign session cookies")
		return 1
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	openCtx, cancel := context.WithTimeout(ctx, commandTimeout)
	pool, err := db.Open(openCtx, db.URL(cfg), cfg.DatabasePool, cfg.PreparedStatements)
	if err == nil {
		err = pool.Ping(openCtx)
	}
	cancel()
	if err != nil {
		fmt.Fprintf(stderr, "serve: connect to database %q: %v\n", cfg.DatabaseName, err)
		return 1
	}
	defer pool.Close()

	ln, err := net.Listen("tcp", ":"+cfg.Port)
	if err != nil {
		fmt.Fprintf(stderr, "serve: %v\n", err)
		return 1
	}
	if err := runServer(ctx, cfg, pool, ln, !*noJobs); err != nil {
		fmt.Fprintf(stderr, "serve: %v\n", err)
		return 1
	}
	return 0
}

// runServer serves on ln and, with withJobs, runs the workers, until ctx is
// cancelled. It then stops the listener and waits for in-flight requests
// (shutdownGrace), and only then stops the workers, which finish their running
// jobs (jobs.DefaultShutdownGrace): an expansion queued by a request that was
// still in flight is not lost, and a restart never cuts a job short of its
// database update.
func runServer(ctx context.Context, cfg config.Config, pool *pgxpool.Pool, ln net.Listener, withJobs bool) error {
	srv := &http.Server{
		Handler:           web.New(cfg, pool),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}

	jobsCtx, stopJobs := context.WithCancel(context.WithoutCancel(ctx))
	defer stopJobs()
	jobsDone := make(chan struct{})
	if withJobs {
		runner, err := newRunner(cfg, pool)
		if err != nil {
			return err
		}
		go func() {
			defer close(jobsDone)
			runner.Run(jobsCtx)
		}()
		slog.Info("serve: job workers started", "workers", runner.Workers)
	} else {
		close(jobsDone)
		slog.Info("serve: job workers are off (--no-jobs)")
	}

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()
	slog.Info("serve: listening", "addr", ln.Addr().String(), "env", cfg.AppEnv, "files", cfg.FilesDir)

	var result error
	select {
	case err := <-serveErr:
		// The listener failed on its own (Serve only returns before Shutdown
		// when it broke).
		result = err
	case <-ctx.Done():
		slog.Info("serve: shutting down")
		shutCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		if err := srv.Shutdown(shutCtx); err != nil {
			slog.Warn("serve: shutdown did not finish cleanly", "err", err)
			_ = srv.Close()
		}
		cancel()
		if err := <-serveErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
			result = err
		}
	}
	stopJobs()
	<-jobsDone
	return result
}

// newRunner builds the job runner with the three job kinds, their sweepers and
// the per-kind timeouts.
func newRunner(cfg config.Config, pool *pgxpool.Pool) (*jobs.Runner, error) {
	expansionDeps := jobs.ExpansionDeps{
		DB: pool,
		Processor: &expansion.Processor{
			FilesDir: cfg.FilesDir,
			DB:       pool,
			Expander: llm.NewExpandService(cfg.ClaudeModel, cfg.SotaLLMsKey),
		},
	}
	videoDeps := jobs.VideoDeps{
		DB:          pool,
		Notifier:    video.NewSlackNotifier(cfg.SlackSuccessURL, cfg.SlackFailureURL),
		Ytdlp:       video.NewYtdlpClient(cfg.YtdlpBin),
		Cookies:     video.NewEgoCookies(),
		NewUploader: jobs.UploaderSource(pool, cfg.YoutubeClientID, cfg.YoutubeClientSecret, cfg.YoutubeRefreshToken),
		Summarizer:  llm.NewSummarizer(llm.NewClient(cfg.SotaLLMsKey)),
		Rulinky:     jobs.NewLinkPublisher(cfg.RulinkyHost, cfg.RulinkyToken),
		Writer:      &files.HTMLWriter{Dir: cfg.FilesDir, DB: pool},
		VideosDir:   cfg.VideosDir,
		Host:        cfg.Host, YoutubeHost: cfg.YoutubeHost, YoutubeReauthURL: cfg.YoutubeReauthURL,
	}

	handlers := jobs.ExpansionHandlers(expansionDeps)
	videoHandlers, err := jobs.VideoHandlers(videoDeps)
	if err != nil {
		return nil, err
	}
	for kind, h := range videoHandlers {
		handlers[kind] = h
	}
	videoSweepers, err := jobs.VideoSweepers(videoDeps)
	if err != nil {
		return nil, err
	}
	return &jobs.Runner{
		DB:       pool,
		Handlers: handlers,
		Workers:  cfg.JobConcurrency,
		Timeouts: jobs.VideoTimeouts(),
		Sweepers: append(jobs.ExpansionSweepers(expansionDeps), videoSweepers...),
	}, nil
}

// syncFiles is ServedFile.sync!, which the Ruby app ran from rails runner and
// the disabled file watcher: it makes served_files match the files directory.
func syncFiles(cfg config.Config, stdout, stderr io.Writer) int {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	pool, err := db.Open(ctx, db.URL(cfg), 1, true)
	if err != nil {
		fmt.Fprintf(stderr, "sync-files: %v\n", err)
		return 1
	}
	defer pool.Close()
	if err := store.SyncServedFiles(ctx, pool, cfg.FilesDir); err != nil {
		fmt.Fprintf(stderr, "sync-files: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "Synced %s\n", cfg.FilesDir)
	return 0
}
