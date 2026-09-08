// Command criteria-adapter-shell is the standalone out-of-process shell adapter
// binary. It serves the protocol-v2 shell adapter via the public Go SDK.
package main

import "os"

func main() {
	if os.Getenv("CRITERIA_REMOTE_HOST") != "" {
		ServeRemoteLoop()
		return
	}
	Serve()
}
