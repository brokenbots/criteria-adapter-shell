package main

import (
	"errors"
	"testing"
)

func TestRunAdapter_RemoteBranch(t *testing.T) {
	t.Setenv("CRITERIA_REMOTE_HOST", "localhost:7778")

	var calledHost string
	serveRemote := func(host string) error {
		calledHost = host
		return nil
	}
	serveLocal := func() {
		t.Error("serveLocal should not be called when CRITERIA_REMOTE_HOST is set")
	}

	if err := runAdapter(serveLocal, serveRemote); err != nil {
		t.Fatalf("runAdapter: %v", err)
	}
	if calledHost != "localhost:7778" {
		t.Errorf("serveRemote called with host %q, want localhost:7778", calledHost)
	}
}

func TestRunAdapter_RemoteBranchError(t *testing.T) {
	t.Setenv("CRITERIA_REMOTE_HOST", "localhost:7778")

	wantErr := errors.New("dial failed")
	serveRemote := func(host string) error {
		return wantErr
	}
	serveLocal := func() {
		t.Error("serveLocal should not be called when CRITERIA_REMOTE_HOST is set")
	}

	err := runAdapter(serveLocal, serveRemote)
	if err != wantErr {
		t.Fatalf("runAdapter error = %v, want %v", err, wantErr)
	}
}

func TestRunAdapter_LocalBranch(t *testing.T) {
	var called bool
	serveLocal := func() {
		called = true
	}
	serveRemote := func(host string) error {
		t.Errorf("serveRemote should not be called when CRITERIA_REMOTE_HOST is unset, called with %q", host)
		return nil
	}

	if err := runAdapter(serveLocal, serveRemote); err != nil {
		t.Fatalf("runAdapter: %v", err)
	}
	if !called {
		t.Error("serveLocal was not called")
	}
}
