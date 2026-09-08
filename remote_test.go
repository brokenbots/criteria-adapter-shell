package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestBuildRemoteOptions_RemoteDefaults(t *testing.T) {
	t.Setenv("CRITERIA_REMOTE_HOST", "localhost:7778")
	t.Setenv("CRITERIA_REMOTE_DIGEST", "sha256:abc123")

	opts, err := buildRemoteOptions("localhost:7778")
	if err != nil {
		t.Fatalf("buildRemoteOptions: %v", err)
	}
	if opts.Host != "localhost:7778" {
		t.Errorf("Host = %q, want localhost:7778", opts.Host)
	}
	if !opts.Reconnect {
		t.Error("Reconnect = false, want true")
	}
	if opts.TLSConfig != nil {
		t.Error("TLSConfig should be nil when no TLS env vars are set")
	}
	if opts.AcceptToken != "" {
		t.Errorf("AcceptToken = %q, want empty", opts.AcceptToken)
	}
	if opts.Identity.Name != Name {
		t.Errorf("Identity.Name = %q, want %q", opts.Identity.Name, Name)
	}
	if opts.Identity.Version != Version {
		t.Errorf("Identity.Version = %q, want %q", opts.Identity.Version, Version)
	}
	if opts.Identity.Digest != "sha256:abc123" {
		t.Errorf("Identity.Digest = %q, want sha256:abc123", opts.Identity.Digest)
	}
}

func TestBuildRemoteOptions_TokenFromEnv(t *testing.T) {
	t.Setenv("CRITERIA_REMOTE_HOST", "host:7778")
	t.Setenv("CRITERIA_REMOTE_TOKEN", "bearer-token")

	opts, err := buildRemoteOptions("host:7778")
	if err != nil {
		t.Fatalf("buildRemoteOptions: %v", err)
	}
	if opts.AcceptToken != "bearer-token" {
		t.Errorf("AcceptToken = %q, want bearer-token", opts.AcceptToken)
	}
}

func TestBuildRemoteOptions_TokenFromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "token")
	if err := os.WriteFile(path, []byte("csi-token\n"), 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}

	t.Setenv("CRITERIA_REMOTE_HOST", "host:7778")
	t.Setenv("CRITERIA_REMOTE_TOKEN_FILE", path)

	opts, err := buildRemoteOptions("host:7778")
	if err != nil {
		t.Fatalf("buildRemoteOptions: %v", err)
	}
	if opts.AcceptToken != "csi-token" {
		t.Errorf("AcceptToken = %q, want csi-token", opts.AcceptToken)
	}
}

func TestBuildRemoteOptions_TokenFileMissing(t *testing.T) {
	t.Setenv("CRITERIA_REMOTE_HOST", "host:7778")
	t.Setenv("CRITERIA_REMOTE_TOKEN_FILE", "/nonexistent/token")

	_, err := buildRemoteOptions("host:7778")
	if err == nil {
		t.Fatal("expected error for missing token file")
	}
}

func TestLoadRemoteTLS_NoConfig(t *testing.T) {
	t.Setenv("CRITERIA_REMOTE_TLS_CERT", "")
	t.Setenv("CRITERIA_REMOTE_TLS_KEY", "")
	t.Setenv("CRITERIA_REMOTE_CA", "")

	conf, err := loadRemoteTLS()
	if err != nil {
		t.Fatalf("loadRemoteTLS: %v", err)
	}
	if conf != nil {
		t.Error("expected nil TLS config when no paths are set")
	}
}

func TestLoadRemoteTLS_PartialConfig(t *testing.T) {
	t.Setenv("CRITERIA_REMOTE_TLS_CERT", "/tmp/cert.pem")
	t.Setenv("CRITERIA_REMOTE_TLS_KEY", "")
	t.Setenv("CRITERIA_REMOTE_CA", "")

	_, err := loadRemoteTLS()
	if err == nil {
		t.Fatal("expected error for partial TLS config")
	}
}

func TestLoadRemoteTLS_FullConfig(t *testing.T) {
	certPath, keyPath, caPath := writeTestKeyPair(t)

	t.Setenv("CRITERIA_REMOTE_TLS_CERT", certPath)
	t.Setenv("CRITERIA_REMOTE_TLS_KEY", keyPath)
	t.Setenv("CRITERIA_REMOTE_CA", caPath)

	conf, err := loadRemoteTLS()
	if err != nil {
		t.Fatalf("loadRemoteTLS: %v", err)
	}
	if conf == nil {
		t.Fatal("expected non-nil TLS config")
	}
	if len(conf.Certificates) != 1 {
		t.Errorf("Certificates length = %d, want 1", len(conf.Certificates))
	}
	if conf.RootCAs == nil {
		t.Error("RootCAs should be set")
	}
}

// writeTestKeyPair creates a self-signed ECDSA certificate/key pair and
// writes them as PEM files, returning the cert, key, and CA paths. The same
// cert is used as the CA root for the test.
func writeTestKeyPair(t *testing.T) (certPath, keyPath, caPath string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth},
		IsCA:         true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}

	dir := t.TempDir()
	certPath = filepath.Join(dir, "cert.pem")
	keyPath = filepath.Join(dir, "key.pem")
	caPath = filepath.Join(dir, "ca.pem")

	writePEM := func(path string, blockType string, bytes []byte) {
		f, err := os.Create(path)
		if err != nil {
			t.Fatalf("create %s: %v", path, err)
		}
		defer f.Close()
		if err := pem.Encode(f, &pem.Block{Type: blockType, Bytes: bytes}); err != nil {
			t.Fatalf("encode %s: %v", path, err)
		}
	}

	keyBytes, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatalf("marshal private key: %v", err)
	}

	writePEM(certPath, "CERTIFICATE", der)
	writePEM(caPath, "CERTIFICATE", der)
	writePEM(keyPath, "PRIVATE KEY", keyBytes)

	return certPath, keyPath, caPath
}
