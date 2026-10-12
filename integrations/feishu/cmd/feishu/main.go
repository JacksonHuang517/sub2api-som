package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"som/feishu/internal/alerts"
	"som/feishu/internal/domain"
	"som/feishu/internal/feishu"
	"som/feishu/internal/outbox"
	"som/feishu/internal/server"
	"som/feishu/internal/som"
	"som/feishu/internal/store"
	"som/feishu/internal/subscriptions"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

var modeOverride = flag.String("mode", "", "Explicit mode: production or mock")

func run() error {
	mode := env("FEISHU_MODE", "production")
	if *modeOverride != "" {
		mode = *modeOverride
	}
	mock := mode == "mock"
	if !mock && mode != "production" {
		return errors.New("invalid mode")
	}
	addr := env("FEISHU_LISTEN", "127.0.0.1:8390")
	host, _, e := net.SplitHostPort(addr)
	if e != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
		return errors.New("listen must be loopback; use reviewed reverse proxy")
	}
	origin := env("FEISHU_ORIGIN", "http://127.0.0.1:5186")
	u, e := url.Parse(origin)
	if e != nil || u.Host == "" || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return errors.New("invalid origin")
	}
	if mock {
		ip := net.ParseIP(u.Hostname())
		if ip == nil || !ip.IsLoopback() || u.Scheme != "http" {
			return errors.New("mock origin must be loopback http")
		}
	} else if u.Scheme != "https" {
		return errors.New("production origin requires https")
	}
	key := os.Getenv("FEISHU_PROOF_KEY")
	if mock && key == "" {
		b := make([]byte, 32)
		if _, e = rand.Read(b); e != nil {
			return e
		}
		key = hex.EncodeToString(b)
	}
	if len(key) < 32 {
		return errors.New("proof key must have at least 32 bytes")
	}
	path := env("FEISHU_DB", "./data/feishu.db")
	if e = os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return e
	}
	lock, e := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return e
	}
	defer lock.Close()
	if e = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		return errors.New("database already owned by another instance")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	db, e := store.Open(path)
	if e != nil {
		return errors.New("database initialization failed")
	}
	defer db.Close()
	var fs domain.Feishu
	var balances domain.Balances
	var quotas domain.Subscriptions
	var fixture *server.Mock
	enabled := mock || os.Getenv("FEISHU_BINDING_ENABLED") == "true"
	if mock {
		fixture = &server.Mock{Origin: origin}
		fixture.Set(20000000)
		fs = fixture
		balances = fixture
		quotas = fixture
	} else {
		fs, e = feishu.New(feishu.Config{AppID: os.Getenv("FEISHU_APP_ID"), AppSecret: os.Getenv("FEISHU_APP_SECRET"), TenantKey: os.Getenv("FEISHU_TENANT_KEY"), RedirectURL: origin + "/integrations/feishu/api/oauth/callback", SiteURL: origin})
		if e != nil {
			return errors.New("Feishu configuration incomplete")
		}
		balances, e = som.New(os.Getenv("SOM_ORIGIN"), os.Getenv("FEISHU_BALANCE_READ_KEY"))
		if e != nil {
			return e
		}
		quotas = balances.(*som.Client)
	}
	app, tenant := os.Getenv("FEISHU_APP_ID"), os.Getenv("FEISHU_TENANT_KEY")
	if mock {
		app, tenant = "mock-app", "mock-tenant"
	}
	if e = store.InvalidateOtherNamespaces(context.Background(), db, app, tenant); e != nil {
		return errors.New("namespace validation failed")
	}
	allowed := map[int64]bool{}
	for _, v := range strings.Split(os.Getenv("FEISHU_SEND_USER_IDS"), ",") {
		if v == "" {
			continue
		}
		id, e := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if e != nil || id <= 0 {
			return errors.New("invalid send allowlist")
		}
		allowed[id] = true
	}
	sending := !mock && os.Getenv("FEISHU_SENDING_ENABLED") == "true"
	if sending && len(allowed) == 0 {
		return errors.New("sending requires explicit user allowlist")
	}
	poll, e := strconv.Atoi(env("FEISHU_POLL_SECONDS", "120"))
	if e != nil || poll < 60 || poll > 300 {
		return errors.New("poll interval must be 60..300 seconds")
	}
	scanner := alerts.New(db, balances, alerts.Config{})
	worker := outbox.New(db, balances, fs, outbox.Config{SendingEnabled: sending, AllowedUsers: allowed, SiteURL: origin})
	quotaScanner := subscriptions.NewScanner(db, quotas, nil)
	quotaWorker := subscriptions.NewWorker(db, quotas, fs, subscriptions.Config{SendingEnabled: sending, AllowedUsers: allowed, SiteURL: origin})
	tick := func(ctx context.Context) error {
		return errors.Join(scanner.Scan(ctx), worker.Drain(ctx), quotaScanner.Scan(ctx), quotaWorker.Drain(ctx))
	}
	handler := &server.Server{DB: db, Feishu: fs, Subscriptions: quotas, Cfg: server.Config{Origin: origin, ProofKey: key, Enabled: enabled, Mock: mock}, Tick: tick}
	if mock {
		handler.MockBalance = fixture.Set
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	srv := &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 45 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 16384}
	var workers sync.WaitGroup
	every := func(interval time.Duration, job func(context.Context)) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			timer := time.NewTicker(interval)
			defer timer.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-timer.C:
					job(ctx)
				}
			}
		}()
	}
	every(time.Duration(poll)*time.Second, func(ctx context.Context) {
		if e := scanner.Scan(ctx); e != nil {
			log.Print("balance scan failed (see status)")
		}
	})
	every(10*time.Second, func(ctx context.Context) {
		if e := worker.Drain(ctx); e != nil {
			log.Print("outbox drain failed (see status)")
		}
	})
	every(time.Duration(poll)*time.Second, func(ctx context.Context) {
		if e := quotaScanner.Scan(ctx); e != nil {
			log.Print("subscription scan failed (see status)")
		}
	})
	every(10*time.Second, func(ctx context.Context) {
		if e := quotaWorker.Drain(ctx); e != nil {
			log.Print("subscription outbox drain failed (see status)")
		}
	})
	every(time.Hour, func(ctx context.Context) {
		_, _ = db.ExecContext(ctx, "DELETE FROM oauth_attempts WHERE expires_at<?", time.Now().Unix())
		_, _ = db.ExecContext(ctx, "DELETE FROM audit_events WHERE at<?", time.Now().Add(-90*24*time.Hour).Unix())
	})
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	log.Printf("SOM Feishu listening on %s mode=%s sending=%t", addr, mode, sending)
	err := srv.ListenAndServe()
	stop()
	workers.Wait()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
func main() {
	flag.Parse()
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, "startup failed:", e)
		os.Exit(1)
	}
}
