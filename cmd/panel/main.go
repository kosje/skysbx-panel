// Command panel runs the skysbx control plane: admin UI, subscriptions and the
// node control channel, backed by a single SQLite file.
package main

import (
	"context"
	"errors"
	"flag"
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

	"github.com/kosje/skysbx-panel/internal/hub"
	"github.com/kosje/skysbx-panel/internal/service"
	"github.com/kosje/skysbx-panel/internal/store"
	"github.com/kosje/skysbx-panel/internal/web"
)

// version is stamped by the installer with the commit it built from, so an
// operator can tell what is actually running without reading a build log.
var version = "dev"

func main() {
	var (
		addr     = flag.String("addr", "127.0.0.1:8080", "address to listen on")
		dbPath   = flag.String("db", "skysbx.db", "path to the SQLite database")
		logLevel = flag.String("log", "info", "log level: debug, info, warn, error")
		insecure = flag.Bool("insecure-cookies", false,
			"send session cookies without the Secure flag (plain HTTP; development only)")
		domain = flag.String("domain", "",
			"serve HTTPS for this domain, obtaining a certificate over ACME. "+
				"Needs ports 80 and 443, and the domain must already resolve here")
		acmeEmail = flag.String("acme-email", "",
			"contact address for the certificate authority (recommended)")
		showVersion = flag.Bool("version", false, "print the version and exit")
	)
	flag.Parse()

	if *showVersion {
		fmt.Printf("skysbx-panel %s\n", version)
		return
	}

	log := newLogger(*logLevel)

	st, err := store.Open(*dbPath)
	if err != nil {
		log.Error("open database", "path", *dbPath, "error", err)
		os.Exit(1)
	}
	defer st.Close()
	log.Info("database ready", "path", *dbPath)

	svc := service.New(st)

	// The hub holds the node control channel. Wiring it as the service's
	// notifier is what turns an edit in the UI into a push to every node; without
	// it the service quietly talks to a no-op and nothing ever reaches a node.
	nodeHub := hub.New(svc, log)
	svc.SetNotifier(nodeHub)

	// A Secure cookie is discarded by the browser over plain HTTP, so binding
	// to localhost for development implies insecure cookies. Anything else has
	// to say so explicitly, which keeps the unsafe choice visible in the
	// command line rather than inferred.
	// Serving ACME TLS means HTTPS, so the cookie must be Secure regardless of
	// what -addr looks like.
	secureCookies := *domain != "" || (!*insecure && !isLoopback(*addr))

	srv, err := web.New(svc, nodeHub, log, secureCookies)
	if err != nil {
		log.Error("build web server", "error", err)
		os.Exit(1)
	}

	if ok, _ := svc.AdminExists(); !ok {
		log.Warn("no administrator configured; open the panel and finish setup",
			"url", "http://"+*addr+"/setup")
	}

	listenAddr := *addr
	if *domain != "" {
		listenAddr = ":443"
	}
	httpSrv := &http.Server{
		Addr:              listenAddr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var redirectSrv *http.Server
	if *domain != "" {
		autoTLS, err := web.NewAutoTLS(*domain, *acmeEmail, filepath.Dir(*dbPath))
		if err != nil {
			log.Error("automatic TLS", "domain", *domain, "error", err)
			os.Exit(1)
		}
		httpSrv.TLSConfig = autoTLS.TLSConfig()

		// Port 80 answers the ACME challenge and redirects everything else, and
		// it has to be serving before the certificate is asked for: that is the
		// port the CA validates on.
		redirectSrv = &http.Server{
			Addr:              ":80",
			Handler:           autoTLS.ChallengeAndRedirect(),
			ReadHeaderTimeout: 10 * time.Second,
		}
		listening := make(chan struct{})
		go func() {
			ln, err := net.Listen("tcp", redirectSrv.Addr)
			if err != nil {
				log.Error("listen on :80", "error", err)
				close(listening)
				stop()
				return
			}
			close(listening)
			if err := redirectSrv.Serve(ln); err != nil &&
				!errors.Is(err, http.ErrServerClosed) {
				log.Error("serve on :80", "error", err)
				stop()
			}
		}()
		<-listening

		// Blocking here means the panel is either reachable over HTTPS or
		// visibly stuck, rather than accepting connections it cannot complete.
		obtainCtx, cancel := context.WithTimeout(ctx, 4*time.Minute)
		err = autoTLS.Obtain(obtainCtx)
		cancel()
		if err != nil {
			log.Error("automatic TLS", "domain", *domain, "error", err)
		} else {
			log.Info("certificate ready", "domain", *domain)
		}
	}

	// The activity digest grows with users × nodes × hours and nothing else in
	// the schema does, so it is the one table that needs sweeping. Once at
	// startup and daily after: a panel that is restarted often would otherwise
	// never reach the timer.
	go func() {
		for {
			if err := svc.PruneActivity(); err != nil {
				log.Warn("prune activity", "error", err)
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(24 * time.Hour):
			}
		}
	}()

	go func() {
		log.Info("listening", "addr", listenAddr, "tls", *domain != "",
			"secure_cookies", secureCookies || *domain != "")
		var err error
		if *domain != "" {
			err = httpSrv.ListenAndServeTLS("", "")
		} else {
			err = httpSrv.ListenAndServe()
		}
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("listen", "error", err)
			stop()
		}
	}()

	<-ctx.Done()
	log.Info("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if redirectSrv != nil {
		redirectSrv.Shutdown(shutdownCtx)
	}
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		log.Warn("shutdown", "error", err)
	}
}

func newLogger(level string) *slog.Logger {
	var l slog.Level
	if err := l.UnmarshalText([]byte(level)); err != nil {
		l = slog.LevelInfo
	}
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: l}))
}

func isLoopback(addr string) bool {
	host := addr
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		host = addr[:i]
	}
	host = strings.Trim(host, "[]")
	return host == "127.0.0.1" || host == "localhost" || host == "::1" || host == ""
}
