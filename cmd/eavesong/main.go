// Command eavesong is a self-hosted product monitor: it watches Hacker News,
// Reddit, Lobsters, GitHub and Bluesky for posts about your product and your
// competitors, drafts replies with your own AI provider, and reports what people
// are saying. It only reads; it never posts.
//
//	eavesong serve     run the web interface and the scheduled checks
//	eavesong check     run one check of every project and exit
//
// Configuration is through environment variables; see the README.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/sonic0002/eavesong/internal/config"
	"github.com/sonic0002/eavesong/internal/monitor"
	"github.com/sonic0002/eavesong/internal/notify"
	"github.com/sonic0002/eavesong/internal/secret"
	"github.com/sonic0002/eavesong/internal/store"
	"github.com/sonic0002/eavesong/internal/web"
)

func env(key, fallback string) string {
	if v := config.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	if err := run(log); err != nil {
		log.Error(err.Error())
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	command := "serve"
	if len(os.Args) > 1 {
		command = os.Args[1]
	}
	interval, err := time.ParseDuration(env("EAVESONG_INTERVAL", "30m"))
	if err != nil || interval < 5*time.Minute {
		return errors.New("EAVESONG_INTERVAL must be a duration of at least 5m, such as 30m")
	}
	dbPath := env("EAVESONG_DB", "eavesong.db")
	db, err := store.Open(dbPath)
	if err != nil {
		return fmt.Errorf("opening the database: %w", err)
	}
	defer db.Close()
	// API keys and tokens are encrypted with a key kept next to the database.
	key, err := secret.LoadKey(dbPath + ".key")
	if err != nil {
		return err
	}
	box, err := secret.New(key)
	if err != nil {
		return err
	}
	settings := config.Manager{Store: db, Box: box, Verify: config.LiveVerifier{}}
	if err := settings.Migrate(context.Background()); err != nil {
		return fmt.Errorf("moving saved accounts: %w", err)
	}
	cfg, err := settings.Load(context.Background())
	if err != nil {
		return err
	}
	if err := cfg.Validate(); err != nil {
		log.Warn("reply drafts are off until the AI settings are fixed", "problem", err)
	}
	accounts, err := settings.Accounts(context.Background())
	if err != nil {
		return err
	}
	draft, aiName := cfg.Drafter()
	m := &monitor.Monitor{Store: db, Sources: config.Sources(accounts, config.XBudget{Store: db, Cap: cfg.XCap()}), Drafter: draft, Interval: interval, Log: log}
	m.Notifier = &notify.Service{Store: db, Config: settings}
	m.ConfigureSearch(config.SearchClients(accounts))
	m.ConfigureAnalytics(config.AnalyticsClients(accounts))
	m.ConfigureScopes(config.Scopes(accounts))
	m.ConfigureOwn(config.OwnNames(accounts))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch command {
	case "check":
		m.CheckAll(ctx)
		return nil
	case "serve":
	default:
		return fmt.Errorf("unknown command %q (use serve or check)", command)
	}

	addr := env("EAVESONG_ADDR", "127.0.0.1:8080")
	password := config.Getenv("EAVESONG_PASSWORD")
	if password == "" {
		host, _, _ := net.SplitHostPort(addr)
		if host != "127.0.0.1" && host != "localhost" && host != "::1" {
			return errors.New("set EAVESONG_PASSWORD before listening on " + addr + "; without it anyone who can reach the port can use Eavesong")
		}
		log.Warn("EAVESONG_PASSWORD is not set; the interface is open to anyone on this machine")
	}
	server := &web.Server{Store: db, Monitor: m, Config: settings, Password: password, Log: log}
	httpServer := &http.Server{Addr: addr, Handler: server.Handler(), ReadHeaderTimeout: 10 * time.Second}

	// The monitor must finish its current check before the database closes.
	monitorDone := make(chan struct{})
	go func() {
		m.Run(ctx)
		close(monitorDone)
	}()
	defer func() { <-monitorDone }()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		httpServer.Shutdown(shutdown)
	}()
	log.Info("Eavesong is running", "url", "http://"+addr, "sources", strings.Join(m.SourceNames(), ","), "drafts", firstNonEmpty(aiName, "off"))
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
