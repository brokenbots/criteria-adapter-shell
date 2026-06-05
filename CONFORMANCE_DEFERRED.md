# Deferred: conformance_test.go

`conformance_test.go` runs the Criteria host's shared conformance suite against
the shell binary. It imports the host's internal test harness
(`github.com/brokenbots/criteria/internal/adapter/conformance`), which is not
importable from this standalone module, so it cannot build on `main`.

Preserved here until the conformance harness is published as a consumable
package. Unit tests (`execute_test.go`) on `main` provide standalone coverage.

Provenance: monorepo `cmd/criteria-adapter-shell/conformance_test.go`,
preserved 2026-06-05 during the WS42 extraction.
