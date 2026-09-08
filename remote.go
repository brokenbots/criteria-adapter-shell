package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	adapterhost "github.com/brokenbots/criteria-go-adapter-sdk/adapterhost"
)

// remoteEnv holds the parsed environment variables used to configure a remote
// (phone-home) adapter connection.
type remoteEnv struct {
	host           string
	token          string
	digest         string
	adapterName    string
	adapterVersion string
	tlsConfig      *tls.Config
}

// ServeRemoteLoop runs the shell adapter as a remote phone-home adapter. It
// reads configuration from the environment, connects to the Criteria host using
// adapterhost.ServeRemote, and reconnects with exponential backoff when the
// connection closes. The loop exits on SIGINT or SIGTERM.
func ServeRemoteLoop() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	opts, err := buildRemoteOptions()
	if err != nil {
		fmt.Fprintf(os.Stderr, "remote adapter: %v\n", err)
		os.Exit(1)
	}

	b := newBackoff(time.Second, 30*time.Second)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		if err := adapterhost.ServeRemote(NewService(), opts); err != nil {
			fmt.Fprintf(os.Stderr, "remote adapter: serve: %v\n", err)
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(b.next()):
		}
	}
}

// buildRemoteOptions constructs ServeRemoteOptions from the process
// environment. CRITERIA_REMOTE_HOST is required; identity defaults to the
// shell adapter's Name and Version constants.
func buildRemoteOptions() (*adapterhost.ServeRemoteOptions, error) {
	cfg, err := parseRemoteEnv()
	if err != nil {
		return nil, err
	}
	return &adapterhost.ServeRemoteOptions{
		Host:        cfg.host,
		TLSConfig:   cfg.tlsConfig,
		AcceptToken: cfg.token,
		Identity: adapterhost.RemoteIdentity{
			Name:    cfg.adapterName,
			Version: cfg.adapterVersion,
			Digest:  cfg.digest,
		},
	}, nil
}

// parseRemoteEnv reads remote-mode environment variables and builds the TLS
// configuration when all three TLS paths are provided.
func parseRemoteEnv() (*remoteEnv, error) {
	cfg := &remoteEnv{
		host:           os.Getenv("CRITERIA_REMOTE_HOST"),
		token:          os.Getenv("CRITERIA_REMOTE_TOKEN"),
		digest:         os.Getenv("CRITERIA_REMOTE_DIGEST"),
		adapterName:    os.Getenv("CRITERIA_ADAPTER_NAME"),
		adapterVersion: os.Getenv("CRITERIA_ADAPTER_VERSION"),
	}
	if cfg.host == "" {
		return nil, fmt.Errorf("CRITERIA_REMOTE_HOST is required for remote mode")
	}
	if cfg.adapterName == "" {
		cfg.adapterName = Name
	}
	if cfg.adapterVersion == "" {
		cfg.adapterVersion = Version
	}

	tlsConfig, err := buildTLSConfig()
	if err != nil {
		return nil, err
	}
	cfg.tlsConfig = tlsConfig
	return cfg, nil
}

// buildTLSConfig loads a mutual-TLS configuration when CRITERIA_REMOTE_TLS_CERT,
// CRITERIA_REMOTE_TLS_KEY and CRITERIA_REMOTE_CA are all set. If none are set,
// the adapter connects over plain TCP (or Unix when the host is a socket path).
// A partial set returns an error so the caller is not silently downgraded.
func buildTLSConfig() (*tls.Config, error) {
	certPath := os.Getenv("CRITERIA_REMOTE_TLS_CERT")
	keyPath := os.Getenv("CRITERIA_REMOTE_TLS_KEY")
	caPath := os.Getenv("CRITERIA_REMOTE_CA")

	if certPath == "" && keyPath == "" && caPath == "" {
		return nil, nil
	}
	if certPath == "" || keyPath == "" || caPath == "" {
		return nil, fmt.Errorf("CRITERIA_REMOTE_TLS_CERT, CRITERIA_REMOTE_TLS_KEY and CRITERIA_REMOTE_CA must all be set to enable TLS")
	}

	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("load client certificate: %w", err)
	}

	caPEM, err := os.ReadFile(caPath)
	if err != nil {
		return nil, fmt.Errorf("read CA certificate: %w", err)
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		return nil, fmt.Errorf("parse CA certificate")
	}

	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      pool,
		MinVersion:   tls.VersionTLS12,
	}, nil
}

// backoff is a simple capped exponential backoff used between reconnects.
type backoff struct {
	initial  time.Duration
	max      time.Duration
	current  time.Duration
}

// newBackoff returns a backoff starting at initial and doubling up to max.
func newBackoff(initial, max time.Duration) *backoff {
	return &backoff{
		initial: initial,
		max:     max,
		current: initial,
	}
}

// next returns the next wait duration and advances the internal state.
func (b *backoff) next() time.Duration {
	d := b.current
	b.current *= 2
	if b.current > b.max {
		b.current = b.max
	}
	return d
}
