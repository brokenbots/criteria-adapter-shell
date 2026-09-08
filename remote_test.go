package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseRemoteEnv_RequiredHost(t *testing.T) {
	t.Setenv("CRITERIA_REMOTE_HOST", "")
	_, err := parseRemoteEnv()
	if err == nil {
		t.Fatal("expected error when CRITERIA_REMOTE_HOST is empty")
	}
}

func TestParseRemoteEnv_Defaults(t *testing.T) {
	t.Setenv("CRITERIA_REMOTE_HOST", "criteria.example.com:7778")

	cfg, err := parseRemoteEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.host != "criteria.example.com:7778" {
		t.Errorf("host = %q, want %q", cfg.host, "criteria.example.com:7778")
	}
	if cfg.adapterName != Name {
		t.Errorf("adapterName = %q, want %q", cfg.adapterName, Name)
	}
	if cfg.adapterVersion != Version {
		t.Errorf("adapterVersion = %q, want %q", cfg.adapterVersion, Version)
	}
	if cfg.tlsConfig != nil {
		t.Error("expected nil TLS config when TLS env vars are absent")
	}
}

func TestParseRemoteEnv_Overrides(t *testing.T) {
	t.Setenv("CRITERIA_REMOTE_HOST", "host.internal:7778")
	t.Setenv("CRITERIA_REMOTE_TOKEN", "secret-token")
	t.Setenv("CRITERIA_REMOTE_DIGEST", "sha256:abc123")
	t.Setenv("CRITERIA_ADAPTER_NAME", "my-shell")
	t.Setenv("CRITERIA_ADAPTER_VERSION", "3.0.0")

	cfg, err := parseRemoteEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.host != "host.internal:7778" {
		t.Errorf("host = %q, want %q", cfg.host, "host.internal:7778")
	}
	if cfg.token != "secret-token" {
		t.Errorf("token = %q, want %q", cfg.token, "secret-token")
	}
	if cfg.digest != "sha256:abc123" {
		t.Errorf("digest = %q, want %q", cfg.digest, "sha256:abc123")
	}
	if cfg.adapterName != "my-shell" {
		t.Errorf("adapterName = %q, want %q", cfg.adapterName, "my-shell")
	}
	if cfg.adapterVersion != "3.0.0" {
		t.Errorf("adapterVersion = %q, want %q", cfg.adapterVersion, "3.0.0")
	}
}

func TestBuildRemoteOptions(t *testing.T) {
	t.Setenv("CRITERIA_REMOTE_HOST", "criteria.example.com:7778")
	t.Setenv("CRITERIA_REMOTE_TOKEN", "token")
	t.Setenv("CRITERIA_REMOTE_DIGEST", "sha256:digest")
	t.Setenv("CRITERIA_ADAPTER_NAME", "shell")
	t.Setenv("CRITERIA_ADAPTER_VERSION", "2.0.1")

	opts, err := buildRemoteOptions()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if opts.Host != "criteria.example.com:7778" {
		t.Errorf("host = %q, want %q", opts.Host, "criteria.example.com:7778")
	}
	if opts.AcceptToken != "token" {
		t.Errorf("token = %q, want %q", opts.AcceptToken, "token")
	}
	if opts.Identity.Name != "shell" {
		t.Errorf("identity name = %q, want %q", opts.Identity.Name, "shell")
	}
	if opts.Identity.Version != "2.0.1" {
		t.Errorf("identity version = %q, want %q", opts.Identity.Version, "2.0.1")
	}
	if opts.Identity.Digest != "sha256:digest" {
		t.Errorf("identity digest = %q, want %q", opts.Identity.Digest, "sha256:digest")
	}
	if opts.TLSConfig != nil {
		t.Error("expected nil TLS config")
	}
}

func TestBuildTLSConfig_PartialSet(t *testing.T) {
	t.Setenv("CRITERIA_REMOTE_TLS_CERT", "/some/cert.pem")
	t.Setenv("CRITERIA_REMOTE_TLS_KEY", "")
	t.Setenv("CRITERIA_REMOTE_CA", "/some/ca.pem")

	_, err := buildTLSConfig()
	if err == nil {
		t.Fatal("expected error for partial TLS env set")
	}
}

func TestBuildTLSConfig_FullSet(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")
	caPath := filepath.Join(dir, "ca.pem")

	certPEM, keyPEM, caPEM := generateTestCerts(t)
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	if err := os.WriteFile(caPath, caPEM, 0o600); err != nil {
		t.Fatalf("write ca: %v", err)
	}

	t.Setenv("CRITERIA_REMOTE_TLS_CERT", certPath)
	t.Setenv("CRITERIA_REMOTE_TLS_KEY", keyPath)
	t.Setenv("CRITERIA_REMOTE_CA", caPath)

	cfg, err := buildTLSConfig()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg == nil {
		t.Fatal("expected TLS config")
	}
	if len(cfg.Certificates) != 1 {
		t.Errorf("certificates = %d, want 1", len(cfg.Certificates))
	}
	if cfg.RootCAs == nil {
		t.Error("expected RootCAs to be set")
	}
	if cfg.MinVersion != tls.VersionTLS12 {
		t.Errorf("min version = %x, want TLS1.2", cfg.MinVersion)
	}
}

func TestBackoff(t *testing.T) {
	b := newBackoff(time.Second, 30*time.Second)
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second, 30 * time.Second}
	for i, d := range want {
		got := b.next()
		if got != d {
			t.Errorf("step %d: got %v, want %v", i, got, d)
		}
	}
}

// generateTestCerts returns a client certificate, its private key, and a CA
// certificate that signed it. All are PEM-encoded.
func generateTestCerts(t *testing.T) (certPEM, keyPEM, caPEM []byte) {
	t.Helper()

	caKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}
	caTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "Test CA"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		IsCA:         true,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTmpl, caTmpl, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create CA cert: %v", err)
	}
	caPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER},
	)

	leafKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate leaf key: %v", err)
	}
	leafTmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "shell-adapter"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTmpl, caTmpl, &leafKey.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create leaf cert: %v", err)
	}
	certPEM = pem.EncodeToMemory(
		&pem.Block{Type: "CERTIFICATE", Bytes: leafDER},
	)
	keyDER, err := x509.MarshalPKCS8PrivateKey(leafKey)
	if err != nil {
		t.Fatalf("marshal leaf key: %v", err)
	}
	keyPEM = pem.EncodeToMemory(
		&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER},
	)
	return certPEM, keyPEM, caPEM
}
