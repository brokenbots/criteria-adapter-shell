# criteria-adapter-shell

A [Criteria](https://github.com/brokenbots/criteria) adapter that runs shell
commands as workflow steps, over the v2 adapter protocol. It is an out-of-process
plugin binary built on the [Go adapter SDK](https://github.com/brokenbots/criteria-go-adapter-sdk)
and the [wire contract](https://github.com/brokenbots/criteria-adapter-proto).

It hardens the spawned process: an env allowlist, `PATH` sanitization
(`command_path`), per-step timeouts, bounded stdout/stderr capture, and
working-directory confinement (under `$HOME` or `CRITERIA_SHELL_ALLOWED_PATHS`).

## Install

The adapter is published as a signed, multi-platform OCI artifact. Pin it in your
workflow and lock it:

```bash
criteria adapter lock <workflow-dir>   # pins digest + signer in .criteria.lock.hcl
```

Supported platforms: `linux/amd64`, `linux/arm64`, `darwin/amd64`, `darwin/arm64`.

## Setup (adapter configuration)

Declare the adapter and bind it to an environment. The shell adapter takes **no
adapter-level `config {}` keys** — all behavior is controlled per step via the
inputs below. The runtime working-directory allowlist is controlled by the host:

```hcl
adapter "shell" "ci" {
  source  = "ghcr.io/brokenbots/criteria-adapter-shell"
  version = "0.5.2"
}
```

| Host control | How | Effect |
| --- | --- | --- |
| `CRITERIA_SHELL_ALLOWED_PATHS` | env var on the adapter process | Extra roots (besides `$HOME`) that `working_directory` may resolve under. |

Secrets referenced by a command are delivered over the Criteria secret channel
(per-session `secrets {}` or per-step secret inputs); their values are redacted
from streamed output.

## Step inputs

| Input | Required | Description |
| --- | --- | --- |
| `command` | **yes** | Command string passed to `sh -c` (Unix) / `cmd /C` (Windows). |
| `env` | no | JSON map of extra env vars. Values starting with `$` inherit from the parent env (e.g. `$GOFLAGS`). `PATH` is reserved — use `command_path`. Build it with `jsonencode({KEY = "$KEY"})`. |
| `command_path` | no | OS-path-separator-delimited directory list that **replaces** `PATH` for the child. |
| `timeout` | no | Hard step timeout, e.g. `10m`. Min `1s`, max `1h`. Default `5m`. |
| `output_limit_bytes` | no | Per-stream capture limit. Range `1024`–`67108864`. Default `4194304` (4 MiB). |
| `working_directory` | no | CWD for the process. Must resolve under `$HOME` or `CRITERIA_SHELL_ALLOWED_PATHS`. |

```hcl
step "build" {
  adapter = adapter.shell.ci
  input {
    command = "go build ./..."
    timeout = "10m"
    env     = jsonencode({ GOFLAGS = "$GOFLAGS" })
  }
}
```

## Config overrides

The shell adapter is **fully step-driven** — every knob (`timeout`,
`output_limit_bytes`, `command_path`, `env`, `working_directory`) is a step
input, so each step overrides the defaults independently. There is no adapter
`config {}` block to override.

## Outputs

| Output | Description |
| --- | --- |
| `stdout` | Captured stdout (bounded by `output_limit_bytes`). |
| `stderr` | Captured stderr (bounded by `output_limit_bytes`). |
| `exit_code` | Process exit code, as a string. |

Outcome mapping: exit `0` → `success`, non-zero (or timeout) → `failure`.

## Build & test

```bash
make build   # go build ./...
make test    # go test ./...
```

The host-driven conformance suite lives on the
[`deferred/conformance`](../../tree/deferred/conformance) branch (it depends on
the Criteria host's internal test harness and cannot build standalone yet).

## Security & dependencies

Supply-chain controls and the dependency-freshness policy are documented in
[SECURITY.md](SECURITY.md) and [docs/dependency-policy.md](docs/dependency-policy.md).
Reproduce the CI security checks locally:

```bash
make vuln-scan      # osv-scanner — known-vulnerability gate (WS49)
make deps-outdated  # go-mod-outdated — freshness report (WS50)
make deps-majors    # gomajor — available major (/vN) upgrades
```

## Publish

Tagging `vX.Y.Z` runs [`.github/workflows/publish.yml`](.github/workflows/publish.yml),
which cross-builds all four platforms and publishes them as a single
multi-platform, signed OCI artifact to
`ghcr.io/brokenbots/criteria-adapter-shell:X.Y.Z` via the reusable
[`brokenbots/publish-adapter`](https://github.com/brokenbots/publish-adapter)
action.

## License

Apache-2.0. See [LICENSE](LICENSE).
