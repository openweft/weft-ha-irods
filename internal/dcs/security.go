// security.go — transport security and authentication of the etcd client.
//
// Sealing (seal.go) protects the CONFIDENTIALITY of the values; this file
// protects integrity and availability: without TLS and authentication anyone
// on the network can read the sealed blobs, delete or overwrite the keys
// (bootstrap lock, seeded configuration).
//
// Secrets are never held here longer than needed: the client private key is
// parsed once, when the store is built, and the PEM is not kept.
package dcs

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
)

// EtcdSecurity configures TLS (optionally mutual) and user authentication of
// the etcd client. The CA and certificate are public file paths; the client
// private key and the password arrive as values read from a secretsource
// reference by the caller (--etcd-client-key-from, --etcd-password-from).
type EtcdSecurity struct {
	// CAFile is a PEM bundle trusted to sign the etcd servers (empty = the
	// system roots).
	CAFile string
	// CertFile and KeyPEM are the client certificate (path) and its private
	// key (PEM content) for mutual TLS. Both or neither.
	CertFile string
	KeyPEM   string
	// Username and Password enable etcd user authentication. Both or neither.
	Username string
	Password string
	// AllowPlaintext lets the client talk to etcd without TLS (and send a
	// password in clear). It exists for a throwaway development etcd only.
	AllowPlaintext bool
}

// wantsTLS reports whether any TLS material was configured.
func (s EtcdSecurity) wantsTLS() bool {
	return s.CAFile != "" || s.CertFile != "" || s.KeyPEM != ""
}

// endpointIsTLS reports whether clientv3 dials ep over TLS: https:// always;
// http:// and unix:// never; a bare host:port only when a TLS config is set.
func endpointIsTLS(ep string, tlsConfigured bool) bool {
	if strings.Contains(ep, "://") {
		u, err := url.Parse(ep)
		return err == nil && (u.Scheme == "https" || u.Scheme == "unixs")
	}
	return tlsConfigured
}

// Check refuses the combinations that leave etcd open or half-configured.
func (s EtcdSecurity) Check(endpoints []string) error {
	if (s.CertFile == "") != (s.KeyPEM == "") {
		return errors.New("etcd client certificate and key must be given together (--etcd-cert and --etcd-client-key-from)")
	}
	if (s.Username == "") != (s.Password == "") {
		return errors.New("etcd user and password must be given together (--etcd-user and --etcd-password-from)")
	}
	if s.AllowPlaintext {
		return nil
	}
	anyHTTPS := false
	for _, ep := range endpoints {
		if strings.Contains(ep, "://") && endpointIsTLS(ep, false) {
			anyHTTPS = true
		}
	}
	tlsOn := s.wantsTLS() || anyHTTPS
	for _, ep := range endpoints {
		if !endpointIsTLS(ep, tlsOn) {
			return fmt.Errorf("etcd endpoint %q is plaintext: use https:// with --etcd-ca (and --etcd-cert/--etcd-client-key-from for mutual TLS), "+
				"or pass --etcd-insecure-plaintext for a throwaway development etcd", redactEndpoint(ep))
		}
	}
	return nil
}

// redactEndpoint drops any userinfo from an endpoint before it is logged.
func redactEndpoint(ep string) string {
	if u, err := url.Parse(ep); err == nil && u.User != nil {
		u.User = nil
		return u.String()
	}
	return ep
}

// tlsConfig builds the client TLS configuration, or nil when none is needed.
func (s EtcdSecurity) tlsConfig(endpoints []string) (*tls.Config, error) {
	need := s.wantsTLS()
	for _, ep := range endpoints {
		if strings.Contains(ep, "://") && endpointIsTLS(ep, false) {
			need = true
		}
	}
	if !need {
		return nil, nil
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS12}
	if s.CAFile != "" {
		pem, err := os.ReadFile(s.CAFile)
		if err != nil {
			return nil, fmt.Errorf("etcd CA: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("etcd CA %s: no PEM certificate found", s.CAFile)
		}
		cfg.RootCAs = pool
	}
	if s.CertFile != "" {
		certPEM, err := os.ReadFile(s.CertFile)
		if err != nil {
			return nil, fmt.Errorf("etcd client certificate: %w", err)
		}
		// The error of X509KeyPair never carries the key.
		cert, err := tls.X509KeyPair(certPEM, []byte(s.KeyPEM))
		if err != nil {
			return nil, fmt.Errorf("etcd client certificate/key: %w", err)
		}
		cfg.Certificates = []tls.Certificate{cert}
	}
	return cfg, nil
}
