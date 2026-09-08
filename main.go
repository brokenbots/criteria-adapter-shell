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
	if err := runAdapter(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func runAdapter() error {
	if host := os.Getenv("CRITERIA_REMOTE_HOST"); host != "" {
		return serveRemote(host)
	}
	Serve()
	return nil
}
