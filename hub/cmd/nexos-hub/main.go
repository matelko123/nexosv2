// Command nexos-hub runs the NexOS web application: the browser UI/API and
// the mTLS client side towards all agents.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"nexos/hub/agents"
	"nexos/hub/alerts"
	"nexos/hub/api"
	"nexos/hub/auth"
	"nexos/hub/certs"
	"nexos/hub/events"
	"nexos/hub/store"
	"nexos/hub/updates"
	"nexos/internal/pki"
	"nexos/web"
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	dataDir := env("NEXOS_DATA_DIR", "/data")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := store.Open(filepath.Join(dataDir, "nexos.db"))
	if err != nil {
		return err
	}
	defer st.Close()

	sealer, err := auth.LoadOrCreateSealer(filepath.Join(dataDir, "secret.key"))
	if err != nil {
		return err
	}

	// Agent CA and the hub's mTLS identity.
	pkiDir := filepath.Join(dataDir, "pki")
	ca, err := pki.LoadOrCreateCA(pkiDir)
	if err != nil {
		return fmt.Errorf("agent CA: %w", err)
	}
	hubCert, err := certs.LoadLeaf(pkiDir, "hub", ca, certs.HubOptions())
	if err != nil {
		return fmt.Errorf("hub certificate: %w", err)
	}
	go hubCert.RenewLoop(ctx)
	slog.Info("agent CA ready", "fingerprint", pki.Fingerprint(ca.Cert))

	interval, err := time.ParseDuration(env("NEXOS_METRICS_INTERVAL", "5s"))
	if err != nil {
		return fmt.Errorf("NEXOS_METRICS_INTERVAL: %w", err)
	}
	var retention store.Retention
	updateInterval := time.Duration(0)
	for _, d := range []struct {
		env, def string
		dst      *time.Duration
	}{
		{"NEXOS_METRICS_RETENTION", "24h", &retention.Raw},
		{"NEXOS_RETENTION_1M", "168h", &retention.OneMinute},
		{"NEXOS_RETENTION_5M", "2160h", &retention.FiveMinute},
		{"NEXOS_RETENTION_CONTAINERS", "168h", &retention.Container},
		{"NEXOS_RETENTION_ALERTS", "2160h", &retention.Events},
		{"NEXOS_UPDATE_CHECK_INTERVAL", "6h", &updateInterval},
	} {
		v, err := time.ParseDuration(env(d.env, d.def))
		if err != nil {
			return fmt.Errorf("%s: %w", d.env, err)
		}
		*d.dst = v
	}

	bus := events.NewBus()
	mgr := agents.NewManager(st, ca, hubCert.KP, bus, interval)
	if err := mgr.Start(ctx); err != nil {
		return err
	}
	go maintainHistory(ctx, st, retention)
	checker := updates.NewChecker(st, mgr, bus, updateInterval)
	go checker.Run(ctx)
	engine := alerts.NewEngine(st, mgr, bus, sealer)
	go engine.Run(ctx)

	// Enrollment endpoint (agents only, intended for the WireGuard address).
	enrollListen := os.Getenv("NEXOS_ENROLL_LISTEN")
	enrollAdvertise := env("NEXOS_ENROLL_ADVERTISE", enrollListen)
	if enrollListen != "" {
		tlsCfg := pki.ServerConfig(hubCert.KP)
		tlsCfg.GetCertificate = hubCert.KP.WithChain(ca.Cert)
		srv := &http.Server{
			Addr: enrollListen, Handler: mgr.EnrollmentHandler(), TLSConfig: tlsCfg,
			ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second,
		}
		go serve(ctx, srv, true, "enrollment")
	} else {
		slog.Warn("enrollment endpoint disabled (NEXOS_ENROLL_LISTEN not set)")
	}

	setupToken := os.Getenv("NEXOS_SETUP_TOKEN")
	if n, err := st.CountUsers(ctx); err != nil {
		return err
	} else if n == 0 {
		if setupToken == "" {
			setupToken = auth.RandomToken(18)
		}
		slog.Warn("no users yet: open the UI and complete setup", "setup_token", setupToken)
	}

	uiTLS := env("NEXOS_UI_TLS", "auto")
	var trusted []*net.IPNet
	for _, cidr := range splitList(os.Getenv("NEXOS_TRUSTED_PROXIES")) {
		_, n, err := net.ParseCIDR(cidr)
		if err != nil {
			return fmt.Errorf("NEXOS_TRUSTED_PROXIES: %w", err)
		}
		trusted = append(trusted, n)
	}
	apiSrv := api.New(api.Config{
		SetupToken:      setupToken,
		EnrollAdvertise: enrollAdvertise,
		SecureCookies:   os.Getenv("NEXOS_INSECURE_COOKIES") != "true",
		HSTS:            uiTLS == "auto",
		TrustedProxies:  trusted,
		WebAuthnOrigins: splitList(os.Getenv("NEXOS_WEBAUTHN_ORIGINS")),
		Static:          web.Dist(),
	}, st, mgr, bus, sealer, engine, checker)
	go apiSrv.Maintenance(ctx)

	ui := &http.Server{
		Addr:              env("NEXOS_UI_LISTEN", ":8443"),
		Handler:           apiSrv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	switch uiTLS {
	case "auto":
		// The UI certificate comes from a separate CA, so trusting it in a
		// browser grants nothing towards the agent mTLS PKI.
		uiDir := filepath.Join(dataDir, "ui-pki")
		uiCA, err := pki.LoadOrCreateCA(uiDir)
		if err != nil {
			return fmt.Errorf("UI CA: %w", err)
		}
		hosts := append([]string{"localhost", "127.0.0.1", "::1"}, splitList(os.Getenv("NEXOS_UI_HOSTS"))...)
		if h, err := os.Hostname(); err == nil {
			hosts = append(hosts, h)
		}
		uiCert, err := certs.LoadLeaf(uiDir, "ui", uiCA, certs.UIOptions(hosts))
		if err != nil {
			return fmt.Errorf("UI certificate: %w", err)
		}
		go uiCert.RenewLoop(ctx)
		ui.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			return uiCert.KP.WithChain(uiCA.Cert)(nil)
		}}
		slog.Info("UI TLS enabled", "ca_file", filepath.Join(uiDir, "ca.crt"), "ca_fingerprint", pki.Fingerprint(uiCA.Cert), "hosts", hosts)
	case "off":
		slog.Warn("UI TLS disabled: serve the hub behind an HTTPS reverse proxy")
	default:
		return fmt.Errorf("NEXOS_UI_TLS must be auto or off")
	}
	serve(ctx, ui, uiTLS == "auto", "ui")
	return nil
}

func serve(ctx context.Context, srv *http.Server, useTLS bool, name string) {
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(sctx)
	}()
	slog.Info("listening", "server", name, "addr", srv.Addr, "tls", useTLS)
	var err error
	if useTLS {
		err = srv.ListenAndServeTLS("", "")
	} else {
		err = srv.ListenAndServe()
	}
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		slog.Error("server failed", "server", name, "err", err)
		os.Exit(1)
	}
}

func maintainHistory(ctx context.Context, st *store.Store, retention store.Retention) {
	// Roll up every minute; prune less often.
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for i := 0; ; i++ {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			if err := st.RollupHostMetrics(ctx, now); err != nil {
				slog.Warn("metrics rollup", "err", err)
			}
			if i%10 == 0 {
				if err := st.PruneHistory(ctx, now, retention); err != nil {
					slog.Warn("prune history", "err", err)
				}
			}
		}
	}
}

func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
