package web

import (
	"crypto/tls"
	"fmt"
	"net/http"
	"path/filepath"

	"github.com/caddyserver/certmagic"
)

// TLSConfig obtains and renews a certificate for domain over ACME, and returns
// a config to serve with.
//
// Doing this in-process is what lets the panel be one binary. A reverse proxy
// in front would mean a second package to install, a second config file to keep
// in step, and a second place for a redirect or a header to go wrong — for a
// single hostname that terminates its own TLS anyway.
//
// The HTTP-01 challenge needs port 80 reachable, which is also where the
// redirect to HTTPS is served from.
func TLSConfig(domain, email, dataDir string) (*tls.Config, error) {
	if domain == "" {
		return nil, fmt.Errorf("a domain is required for automatic TLS")
	}

	cache := certmagic.NewCache(certmagic.CacheOptions{
		GetConfigForCert: func(certmagic.Certificate) (*certmagic.Config, error) {
			return certmagic.New(nil, certmagic.Config{}), nil
		},
	})
	cfg := certmagic.New(cache, certmagic.Config{
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
		DisableTLSALPNChallenge: false,
	})
	cfg.Issuers = []certmagic.Issuer{issuer}

	if err := cfg.ManageSync(nil, []string{domain}); err != nil {
		return nil, fmt.Errorf("obtain certificate for %s: %w", domain, err)
	}

	tlsCfg := cfg.TLSConfig()
	tlsCfg.NextProtos = append([]string{"h2", "http/1.1"}, tlsCfg.NextProtos...)
	return tlsCfg, nil
}

// ChallengeAndRedirect serves the ACME HTTP-01 challenge on port 80 and sends
// everything else to HTTPS.
//
// The challenge has to come first: answering it with a redirect is the classic
// way to make renewal fail three months after anyone last looked.
func ChallengeAndRedirect(domain string) http.Handler {
	issuer := certmagic.NewACMEIssuer(nil, certmagic.ACMEIssuer{})
	return issuer.HTTPChallengeHandler(http.HandlerFunc(
		func(w http.ResponseWriter, r *http.Request) {
			target := "https://" + domain + r.URL.RequestURI()
			http.Redirect(w, r, target, http.StatusMovedPermanently)
		}))
}
