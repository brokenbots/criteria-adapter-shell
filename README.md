# criteria-adapter-shell

A [Criteria](https://github.com/brokenbots/criteria) adapter that runs shell
commands as workflow steps, over the v2 adapter protocol. It is an out-of-process
plugin binary built on the [Go adapter SDK](https://github.com/brokenbots/criteria-go-adapter-sdk)
and the [wire contract](https://github.com/brokenbots/criteria-adapter-proto).

It hardens the spawned process: an env allowlist, `PATH` sanitization
(`command_path`), per-step timeouts, bounded stdout/stderr capture, and
working-directory confinement (under `$HOME` or `CRITERIA_SHELL_ALLOWED_PATHS`).

## Usage

```hcl
adapter "shell" "ci" {}

step "build" {
  adapter = adapter.shell.ci
  input {
    command = "go build ./..."
    timeout = "10m"
  }
}
```

Inputs: `command` (required), `env` (JSON map; `$VAR` values inherit from the
parent env), `command_path`, `timeout`, `output_limit_bytes`,
`working_directory`. Outputs: `stdout`, `stderr`, `exit_code`.

## Build & test

```bash
go build -o bin/criteria-adapter-shell .
go test ./...
```

The host-driven conformance suite lives on the
[`deferred/conformance`](../../tree/deferred/conformance) branch (it depends on
the Criteria host's internal test harness and cannot build standalone yet).

## Publish

Tagging `vX.Y.Z` builds the binary and publishes it as an OCI artifact to
`ghcr.io/brokenbots/criteria-adapter-shell:X.Y.Z` via the reusable
[`brokenbots/publish-adapter`](https://github.com/brokenbots/publish-adapter)
action.

## License

Apache-2.0. See [LICENSE](LICENSE).
