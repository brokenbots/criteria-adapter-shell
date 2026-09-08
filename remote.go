package main

import (
	"crypto/tls"
	"fmt"
	"os"
	"strings"

	adapterhost "github.com/brokenbots/criteria-go-adapter-sdk/adapterhost"
)

// serveRemote connects the shell adapter to a Criteria host shim over TCP or
// Unix, optionally with mTLS. It reconnects automatically when the connection
// drops so Kubernetes sidecars survive host restarts and pod rescheduling.
func serveRemote(host string) error {
	opts, err := buildRemoteOptions(host)
	if err != nil {
		return err
	}
	return adapterhost.ServeRemote(NewService(), opts)
}

// buildRemoteOptions assembles the ServeRemoteOptions from environment
// variables. It is separated from serveRemote so the configuration path can be
// unit-tested without starting a network connection.
func buildRemoteOptions(host string) (*adapterhost.ServeRemoteOptions, error) {
	tlsConf, err := loadRemoteTLS()
	if err != nil {
		return nil, err
	}
	token, err := resolveToken()
	if err != nil {
		return nil, err
	}
	return &adapterhost.ServeRemoteOptions{
		Host:        host,
		TLSConfig:   tlsConf,
		AcceptToken: token,
		Identity: adapterhost.RemoteIdentity{
			Name:    Name,
			Version: Version,
			Digest:  os.Getenv("CRITERIA_REMOTE_DIGEST"),
		},
		Reconnect: true,
	}, nil
}

// loadRemoteTLS builds a TLS config from CSI- or Secret-mounted file paths.
// When none of CRITERIA_REMOTE_TLS_CERT, CRITERIA_REMOTE_TLS_KEY, or
// CRITERIA_REMOTE_CA are set it returns nil so the adapter dials the host
// over plain TCP (or Unix). If only some are set it returns an error: partial
// mTLS configuration would fail in a confusing way on connect.
func loadRemoteTLS() (*tls.Config, error) {
	cert := os.Getenv("CRITERIA_REMOTE_TLS_CERT")
	key := os.Getenv("CRITERIA_REMOTE_TLS_KEY")
	ca := os.Getenv("CRITERIA_REMOTE_CA")
	if cert == "" && key == "" && ca == "" {
		return nil, nil
	}
	if cert == "" || key == "" || ca == "" {
		return nil, fmt.Errorf("remote TLS requires all three of CRITERIA_REMOTE_TLS_CERT, CRITERIA_REMOTE_TLS_KEY, and CRITERIA_REMOTE_CA")
	}
	return adapterhost.LoadClientTLS(cert, key, ca)
}

// resolveToken reads the accept token supplied to the remote handshake. It
// prefers CRITERIA_REMOTE_TOKEN_FILE so Kubernetes CSI and projected-volume
// mounts that expose secrets as files work without extra init-container
// translation; CRITERIA_REMOTE_TOKEN is used as a literal value fallback.
func resolveToken() (string, error) {
	if path := os.Getenv("CRITERIA_REMOTE_TOKEN_FILE"); path != "" {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read CRITERIA_REMOTE_TOKEN_FILE %q: %w", path, err)
		}
		// Kubernetes Secrets and CSI drivers commonly write a trailing newline
		// into mounted secret files; strip it so the handshake token matches the
		// host's exact value.
		return strings.TrimSpace(string(data)), nil
	}
	return os.Getenv("CRITERIA_REMOTE_TOKEN"), nil
}
