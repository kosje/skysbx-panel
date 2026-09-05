package web

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"path/filepath"

	"github.com/caddyserver/certmagic"
)

// AutoTLS obtains and renews the panel's certificate over ACME.
//
// Doing this in-process is what lets the panel be one binary. A reverse proxy
// in front would mean a second package to install, a second config file to keep
// in step, and a second place for a redirect or a header to go wrong — for a
// single hostname that terminates its own TLS anyway.
//
// The three parts have to share one certmagic Config: the HTTP-01 challenge
// state is written to the Config's storage when the challenge starts and read
// back out when the ACME server comes knocking on port 80. A handler built from
// a second, unrelated issuer answers 404 to every challenge it is given.
type AutoTLS struct {
	domain string
	cfg    *certmagic.Config
	issuer *certmagic.ACMEIssuer
}

// NewAutoTLS prepares ACME for domain but does not talk to the CA yet — call
// Obtain for that, after the challenge handler is already listening.
func NewAutoTLS(domain, email, dataDir string) (*AutoTLS, error) {
	if domain == "" {
		return nil, fmt.Errorf("a domain is required for automatic TLS")
	}

	var cfg *certmagic.Config
	cache := certmagic.NewCache(certmagic.CacheOptions{
		GetConfigForCert: func(certmagic.Certificate) (*certmagic.Config, error) {
			return cfg, nil
		},
	})
	cfg = certmagic.New(cache, certmagic.Config{
		// Certificates live beside the database so one directory is the whole
		// of the panel's state, and backing it up means copying one path.
		Storage: &certmagic.FileStorage{Path: filepath.Join(dataDir, "certs")},
	})

	issuer := certmagic.NewACMEIssuer(cfg, certmagic.ACMEIssuer{
		CA:     certmagic.LetsEncryptProductionCA,
		Email:  email,
		Agreed: email != "",
		// DNS-01 would avoid needing port 80, but it needs provider credentials
		// the panel has no reason to hold. HTTP-01 keeps the deployment to one
		// binary and one open port pair.
		DisableHTTPChallenge: false,
	})
	cfg.Issuers = []certmagic.Issuer{issuer}

	return &AutoTLS{domain: domain, cfg: cfg, issuer: issuer}, nil
}

// ChallengeAndRedirect is the port 80 handler: it answers the ACME HTTP-01
// challenge and sends everything else to HTTPS.
//
// The challenge has to come first. Answering it with a redirect is the classic
// way to make renewal fail three months after anyone last looked.
//
// It must be serving before Obtain is called. certmagic's own solver would
// otherwise try to bind port 80 itself, and whichever of the two loses the race
// is the one that fails.
func (a *AutoTLS) ChallengeAndRedirect() http.Handler {
	return a.issuer.HTTPChallengeHandler(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			target := "https://" + a.domain + r.URL.RequestURI()
			http.Redirect(w, r, target, http.StatusMovedPermanently)
		}))
}

// Obtain gets the certificate, blocking until it has one or ctx expires.
//
// A returned error is not fatal: renewal has been handed to certmagic's
// background maintenance, which retries with its own backoff. Exiting instead
// would put the process in a restart loop against a CA that rate-limits
// failures, and burning the hour's budget is a worse state to be in than being
// down while someone reads the log.
func (a *AutoTLS) Obtain(ctx context.Context) error {
	if err := a.cfg.ManageSync(ctx, []string{a.domain}); err != nil {
		if aerr := a.cfg.ManageAsync(context.WithoutCancel(ctx), []string{a.domain}); aerr != nil {
			return fmt.Errorf("obtain certificate for %s: %w", a.domain, err)
		}
		return fmt.Errorf("obtain certificate for %s (retrying in the background): %w",
			a.domain, err)
	}
	return nil
}

// TLSConfig is what to serve HTTPS with. Valid before Obtain succeeds, but
// handshakes fail until a certificate is in the cache.
func (a *AutoTLS) TLSConfig() *tls.Config {
	cfg := a.cfg.TLSConfig()
	cfg.NextProtos = append([]string{"h2", "http/1.1"}, cfg.NextProtos...)
	return cfg
}
