package dcs

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/api/v3/mvccpb"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// pki is a throwaway CA with a server certificate (127.0.0.1) and a client one.
type pki struct {
	dir              string
	caFile, certFile string
	keyPEM           string
	serverCert       tls.Certificate
	caPool           *x509.CertPool
	otherCAFile      string // a CA that signed nothing here
}

func newCert(t *testing.T, cn string, parent *x509.Certificate, parentKey *ecdsa.PrivateKey, isCA bool, usage x509.ExtKeyUsage) (*x509.Certificate, *ecdsa.PrivateKey, []byte, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{usage},
		BasicConstraintsValid: true,
		IsCA:                  isCA,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
	}
	if isCA {
		tpl.KeyUsage |= x509.KeyUsageCertSign
		tpl.ExtKeyUsage = nil
	}
	signer, signKey := tpl, key
	if parent != nil {
		signer, signKey = parent, parentKey
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, signer, &key.PublicKey, signKey)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	kd, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return cert, key,
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kd})
}

func newPKI(t *testing.T) *pki {
	t.Helper()
	p := &pki{dir: t.TempDir()}
	ca, caKey, caPEM, _ := newCert(t, "ca", nil, nil, true, 0)
	_, _, srvPEM, srvKeyPEM := newCert(t, "etcd", ca, caKey, false, x509.ExtKeyUsageServerAuth)
	_, _, cliPEM, cliKeyPEM := newCert(t, "client", ca, caKey, false, x509.ExtKeyUsageClientAuth)
	_, _, otherCAPEM, _ := newCert(t, "other-ca", nil, nil, true, 0)
	var err error
	if p.serverCert, err = tls.X509KeyPair(srvPEM, srvKeyPEM); err != nil {
		t.Fatal(err)
	}
	p.caPool = x509.NewCertPool()
	p.caPool.AddCert(ca)
	write := func(name string, b []byte) string {
		f := filepath.Join(p.dir, name)
		if err := os.WriteFile(f, b, 0o600); err != nil {
			t.Fatal(err)
		}
		return f
	}
	p.caFile = write("ca.pem", caPEM)
	p.certFile = write("client.pem", cliPEM)
	p.otherCAFile = write("other-ca.pem", otherCAPEM)
	p.keyPEM = string(cliKeyPEM)
	return p
}

// fakeEtcd serves the KV and Auth services of etcd over TLS (mTLS when
// requireClient), checking a user/password the way etcd does: Authenticate
// hands out a token the KV service then demands in the "token" metadata.
type fakeEtcd struct {
	etcdserverpb.UnimplementedKVServer
	etcdserverpb.UnimplementedAuthServer
	user, password string
	ranges         int
}

const fakeToken = "tok-123"

func (f *fakeEtcd) Authenticate(_ context.Context, r *etcdserverpb.AuthenticateRequest) (*etcdserverpb.AuthenticateResponse, error) {
	if r.Name != f.user || r.Password != f.password {
		return nil, status.Error(codes.InvalidArgument, "etcdserver: authentication failed, invalid user ID or password")
	}
	return &etcdserverpb.AuthenticateResponse{Token: fakeToken}, nil
}

func (f *fakeEtcd) Range(ctx context.Context, r *etcdserverpb.RangeRequest) (*etcdserverpb.RangeResponse, error) {
	if f.user != "" {
		md, _ := metadata.FromIncomingContext(ctx)
		if v := md.Get("token"); len(v) != 1 || v[0] != fakeToken {
			return nil, status.Error(codes.InvalidArgument, "etcdserver: user name is empty")
		}
	}
	f.ranges++
	return &etcdserverpb.RangeResponse{
		Header: &etcdserverpb.ResponseHeader{Revision: 1},
		Count:  1,
		Kvs:    []*mvccpb.KeyValue{{Key: r.Key, Value: []byte("v")}},
	}, nil
}

func (p *pki) serve(t *testing.T, requireClient bool, f *fakeEtcd) string {
	t.Helper()
	cfg := &tls.Config{Certificates: []tls.Certificate{p.serverCert}, MinVersion: tls.VersionTLS12}
	if requireClient {
		cfg.ClientCAs = p.caPool
		cfg.ClientAuth = tls.RequireAndVerifyClientCert
	}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := grpc.NewServer(grpc.Creds(credentials.NewTLS(cfg)))
	etcdserverpb.RegisterKVServer(srv, f)
	etcdserverpb.RegisterAuthServer(srv, f)
	go func() { _ = srv.Serve(lis) }()
	t.Cleanup(srv.Stop)
	return lis.Addr().String()
}

func getKey(t *testing.T, endpoints []string, sec EtcdSecurity) error {
	t.Helper()
	e, err := NewEtcdStore(endpoints, "inst", 15, sec)
	if err != nil {
		return err
	}
	cli, err := e.dial()
	if err != nil {
		return err
	}
	defer cli.Close()
	c, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err = cli.Get(c, "/k")
	return err
}

func TestEtcdMutualTLSAndAuth(t *testing.T) {
	p := newPKI(t)
	f := &fakeEtcd{user: "weft", password: "s3cret"}
	addr := p.serve(t, true, f)
	ep := []string{"https://" + addr}
	good := EtcdSecurity{CAFile: p.caFile, CertFile: p.certFile, KeyPEM: p.keyPEM, Username: "weft", Password: "s3cret"}

	if err := getKey(t, ep, good); err != nil {
		t.Fatalf("mTLS + auth against the fake: %v", err)
	}
	if f.ranges != 1 {
		t.Fatalf("server saw %d reads, want 1", f.ranges)
	}

	for name, mut := range map[string]func(*EtcdSecurity){
		"no client certificate": func(s *EtcdSecurity) { s.CertFile, s.KeyPEM = "", "" },
		"untrusted server CA":   func(s *EtcdSecurity) { s.CAFile = p.otherCAFile },
		"wrong password":        func(s *EtcdSecurity) { s.Password = "wrong" },
		"no credentials":        func(s *EtcdSecurity) { s.Username, s.Password = "", "" },
	} {
		sec := good
		mut(&sec)
		before := f.ranges
		err := getKey(t, ep, sec)
		if err == nil {
			t.Errorf("%s: the read succeeded", name)
		}
		if f.ranges != before {
			t.Errorf("%s: the server answered a read", name)
		}
		if err != nil && (strings.Contains(err.Error(), "s3cret") || strings.Contains(err.Error(), "PRIVATE KEY")) {
			t.Errorf("%s: error echoes a secret: %v", name, err)
		}
	}
}

func TestEtcdServerOnlyTLS(t *testing.T) {
	p := newPKI(t)
	addr := p.serve(t, false, &fakeEtcd{})
	// A bare host:port is dialled over TLS because a CA is configured.
	if err := getKey(t, []string{addr}, EtcdSecurity{CAFile: p.caFile}); err != nil {
		t.Fatalf("server-only TLS: %v", err)
	}
}

func TestEtcdCheckRefusals(t *testing.T) {
	cases := []struct {
		name string
		eps  []string
		sec  EtcdSecurity
		want string // substring of the error, "" = accepted
	}{
		{"http endpoint", []string{"http://etcd:2379"}, EtcdSecurity{}, "plaintext"},
		{"bare host:port", []string{"etcd:2379"}, EtcdSecurity{}, "plaintext"},
		{"http beside a CA", []string{"http://etcd:2379"}, EtcdSecurity{CAFile: "ca"}, "plaintext"},
		{"one http among https", []string{"https://a:2379", "http://b:2379"}, EtcdSecurity{}, "plaintext"},
		{"password in clear", []string{"http://etcd:2379"}, EtcdSecurity{Username: "u", Password: "p"}, "plaintext"},
		{"http with the explicit flag", []string{"http://etcd:2379"}, EtcdSecurity{AllowPlaintext: true}, ""},
		{"https", []string{"https://etcd:2379"}, EtcdSecurity{}, ""},
		{"bare host:port with a CA", []string{"etcd:2379"}, EtcdSecurity{CAFile: "ca"}, ""},
		{"cert without key", []string{"https://etcd:2379"}, EtcdSecurity{CertFile: "c"}, "together"},
		{"key without cert", []string{"https://etcd:2379"}, EtcdSecurity{KeyPEM: "k"}, "together"},
		{"user without password", []string{"https://etcd:2379"}, EtcdSecurity{Username: "u"}, "together"},
		{"password without user", []string{"https://etcd:2379"}, EtcdSecurity{Password: "p"}, "together"},
	}
	for _, c := range cases {
		err := c.sec.Check(c.eps)
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: refused: %v", c.name, err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%s: err = %v, want %q", c.name, err, c.want)
		}
	}
}

func TestEtcdSecurityBadMaterial(t *testing.T) {
	p := newPKI(t)
	junk := filepath.Join(t.TempDir(), "junk.pem")
	if err := os.WriteFile(junk, []byte("not pem"), 0o600); err != nil {
		t.Fatal(err)
	}
	eps := []string{"https://etcd:2379"}
	if _, err := NewEtcdStore(eps, "i", 15, EtcdSecurity{CAFile: junk}); err == nil {
		t.Error("a CA without PEM was accepted")
	}
	if _, err := NewEtcdStore(eps, "i", 15, EtcdSecurity{CAFile: filepath.Join(p.dir, "missing")}); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a missing CA: %v", err)
	}
	_, err := NewEtcdStore(eps, "i", 15, EtcdSecurity{CertFile: p.certFile, KeyPEM: "SUPER-SECRET-NOT-A-KEY"})
	if err == nil || strings.Contains(err.Error(), "SUPER-SECRET") {
		t.Errorf("a bad key must be refused without echoing it: %v", err)
	}
	if _, err := NewEtcdStore(eps, "i", 15, EtcdSecurity{CertFile: filepath.Join(p.dir, "missing"), KeyPEM: p.keyPEM}); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a missing certificate: %v", err)
	}
}

func TestEtcdPlaintextBuildsNoTLSConfig(t *testing.T) {
	e, err := NewEtcdStore([]string{"http://127.0.0.1:2379"}, "i", 15, EtcdSecurity{AllowPlaintext: true})
	if err != nil {
		t.Fatal(err)
	}
	if e.tlsConfig != nil {
		t.Fatal("a plaintext endpoint got a TLS config")
	}
	e, err = NewEtcdStore([]string{"https://127.0.0.1:2379"}, "i", 15, EtcdSecurity{})
	if err != nil {
		t.Fatal(err)
	}
	if e.tlsConfig == nil || e.tlsConfig.MinVersion < tls.VersionTLS12 {
		t.Fatalf("https endpoint: tls config %+v", e.tlsConfig)
	}
}

func TestEtcdRefusalNeverEchoesEndpointCredentials(t *testing.T) {
	err := EtcdSecurity{}.Check([]string{"http://admin:hunter2@etcd:2379"})
	if err == nil || strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("err = %v", err)
	}
}
