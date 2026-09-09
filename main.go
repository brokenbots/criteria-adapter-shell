// Command criteria-adapter-shell is the standalone out-of-process shell adapter
// binary. It serves the protocol-v2 shell adapter via the public Go SDK.
//
// By default it runs locally under the Criteria host's go-plugin loader. When
// CRITERIA_REMOTE_HOST is set it instead connects to a Criteria host shim as a
// remote sidecar, reconnecting automatically on disconnect.
package main

import (
	"fmt"
	"os"
)

func main() {
	if err := runAdapter(Serve, serveRemote); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// runAdapter chooses between local go-plugin and remote sidecar serving based
// on CRITERIA_REMOTE_HOST. The dependencies are injected so the central
// branch can be unit-tested without touching the network or plugin loader.
func runAdapter(serveLocal func(), serveRemoteFn func(host string) error) error {
	if host := os.Getenv("CRITERIA_REMOTE_HOST"); host != "" {
		return serveRemoteFn(host)
	}
	serveLocal()
	return nil
}
