package mail

import "crypto/tls"

// tlsConfigIgnoringVerification returns a usable TLS config for relays with
// self-signed certificates. Only selected per sending profile.
func tlsConfigIgnoringVerification(insecure bool) *tls.Config {
	cfg := tls.Config{MinVersion: tls.VersionTLS12, ServerName: ""}
	if insecure {
		cfg.InsecureSkipVerify = true
	}
	return &cfg
}
