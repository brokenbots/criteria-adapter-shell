# Multi-Platform Build Fix — criteria-adapter-shell

## Summary

The shell adapter was originally published as a **linux/amd64-only** OCI artifact. This meant the adapter could not be pulled on arm64 Linux hosts or any macOS host (darwin/amd64 or darwin/arm64). Two commits fixed the problem and added a safety gate to prevent regression.

---

## Problem

When the shell adapter was first extracted from the monorepo, its publish workflow and `Info()` declaration both targeted a single platform:

| Layer | Before | Impact |
|---|---|---|
| **`shell.go` Platforms field** | `[]string{"linux/amd64"}` | The OCI manifest listed only `linux/amd64`, so orchestrators on other platforms rejected the image as incompatible. |
| **`publish.yml` build step** | Single `go build` with no `GOOS`/`GOARCH` overrides | Only the host-platform binary (linux/amd64 on `ubuntu-latest`) was produced. The publish action packaged that one binary. |

Any attempt to `criteria adapter pull` on linux/arm64, darwin/amd64, or darwin/arm64 would fail with a "no matching platform" error.

## Root Cause

The adapter is **pure Go** (no CGO, no platform-specific build tags), so it *can* be cross-compiled trivially from any host. The initial publish workflow simply never did so — it built one native binary and stopped.

---

## Fix

### Commit `22ef1c9` — Multi-platform cross-build + Platforms declaration

**`shell.go`** — expanded the Platforms slice:

```go
// Before
Platforms: []string{"linux/amd64"},

// After
Platforms: []string{"linux/amd64", "linux/arm64", "darwin/amd64", "darwin/arm64"},
```

This populates the OCI manifest's platforms list (via `--emit-manifest`), telling the registry and consumers which OS/arch combos the artifact supports.

**`.github/workflows/publish.yml`** — replaced the single-binary build with a cross-compile loop:

```yaml
# Before: single binary
- name: Build adapter binary
  run: |
    mkdir -p bin
    CGO_ENABLED=0 go build -trimpath -o bin/criteria-adapter-shell .
# …
binary: ${{ github.workspace }}/bin/criteria-adapter-shell

# After: cross-compile all four platforms into artifact/bin/<os>/<arch>/
- name: Cross-build adapter binaries
  run: |
    set -euo pipefail
    for platform in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do
      os="${platform%/*}"; arch="${platform#*/}"
      outdir="artifact/bin/${os}/${arch}"
      mkdir -p "$outdir"
      echo "building ${platform}"
      CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" \
        go build -trimpath -o "$outdir/criteria-adapter-shell" .
    done
# …
binary: ${{ github.workspace }}/artifact    # directory, not single file
```

The `brokenbots/publish-adapter@v0` action scans `artifact/bin/<os>/<arch>/` and packages all found binaries into one multi-platform OCI artifact.

### Commit `789576b` — Build verification gate + local cross-build target

This follow-up commit added a safety net against silent cross-build failures:

**`.github/workflows/publish.yml`** — new "Verify multi-platform build" step after the cross-compile:

```yaml
- name: Verify multi-platform build
  run: |
    set -euo pipefail
    missing=0
    for platform in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do
      os="${platform%/*}"; arch="${platform#*/}"
      bin="artifact/bin/${os}/${arch}/criteria-adapter-shell"
      if [ ! -f "$bin" ]; then
        echo "Missing binary for ${platform}: $bin" >&2
        missing=1
      else
        echo "Verified ${platform}: $bin ($(wc -c < "$bin") bytes)"
      fi
    done
    if [ "$missing" -ne 0 ]; then
      exit 1
    fi
```

If any expected binary is missing, the workflow fails *before* the publish step, preventing an incomplete artifact from being pushed.

**`.gitignore`** — added `artifact/` so local cross-build outputs aren't accidentally committed.

**`Makefile`** — added `make cross-build` target for local reproduction:

```makefile
cross-build: ## Cross-build all supported platforms into artifact/bin/<os>/<arch>/
	@for platform in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do \
		os="$${platform%/*}"; arch="$${platform#*/}"; \
		outdir="artifact/bin/$${os}/$${arch}"; \
		mkdir -p "$$outdir"; \
		echo "building $${platform}"; \
		CGO_ENABLED=0 GOOS="$$os" GOARCH="$$arch" \
		  $(GO) build -trimpath -o "$$outdir/criteria-adapter-shell" .; \
	done
```

**`go.mod`** — updated Go directive from `1.26.3` to `1.26.4` to resolve GO-2026-5037 and GO-2026-5037 (osv-scanner findings).

---

## How to Check Other Adapters

Any Criteria adapter that publishes an OCI artifact needs both halves of the fix. Audit each adapter for these two signals:

### 1. Check the Go `Platforms` declaration

```sh
grep -n 'Platforms:' <adapter>.go
```

- **Failing:** `Platforms: []string{"linux/amd64"}` — declares only one platform.
- **Passing:** `Platforms: []string{"linux/amd64", "linux/arm64", "darwin/amd64", "darwin/arm64"}` — declares all four.

### 2. Check the publish workflow's build step

```sh
grep -A5 'go build' .github/workflows/publish.yml
```

- **Failing:** Single `go build` with no `GOOS`/`GOARCH` loop — produces one native binary.
- **Passing:** A loop over `linux/amd64 linux/arm64 darwin/amd64 darwin/arm64` that sets `GOOS` and `GOARCH` and writes each binary to `artifact/bin/<os>/<arch>/`.

### 3. Check the `binary` input to the publish action

```sh
grep 'binary:' .github/workflows/publish.yml
```

- **Failing:** Points to a single binary file (e.g. `${{ github.workspace }}/bin/criteria-adapter-shell`).
- **Passing:** Points to a directory tree (e.g. `${{ github.workspace }}/artifact`).

### 4. Check for a build verification step

```sh
grep -A10 'Verify' .github/workflows/publish.yml
```

- **Missing:** No verification step — a silent cross-build failure could produce an incomplete artifact.
- **Present:** A step that loops over all expected platforms and asserts each binary file exists before proceeding to publish.

### Current adapter status

| Adapter | Platforms field | CI cross-build | Verification gate | Status |
|---|---|---|---|---|
| **shell** | 4 platforms | Yes | Yes | Fixed |
| **copilot** | 4 platforms | Yes | No | Fixed (could add verification) |
| **noop** | 4 platforms | Yes | No | Clean (could add verification) |
| **proto** | N/A (library) | N/A | N/A | Not applicable |

---

## How to Fix (Apply to a New or Broken Adapter)

### Step 1 — Expand the Platforms field

In the adapter's main Go file, find the `InfoResponse()` function and set `Platforms` to all four supported targets:

```go
Platforms: []string{"linux/amd64", "linux/arm64", "darwin/amd64", "darwin/arm64"},
```

### Step 2 — Convert the publish workflow to cross-build

Replace the single-binary build step with the cross-compile loop. The full pattern:

```yaml
- name: Cross-build adapter binaries
  run: |
    set -euo pipefail
    for platform in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do
      os="${platform%/*}"; arch="${platform#*/}"
      outdir="artifact/bin/${os}/${arch}"
      mkdir -p "$outdir"
      echo "building ${platform}"
      CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" \
        go build -trimpath -o "$outdir/<adapter-binary-name>" .
    done

- name: Verify multi-platform build
  run: |
    set -euo pipefail
    missing=0
    for platform in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do
      os="${platform%/*}"; arch="${platform#*/}"
      bin="artifact/bin/${os}/${arch}/<adapter-binary-name>"
      if [ ! -f "$bin" ]; then
        echo "Missing binary for ${platform}: $bin" >&2
        missing=1
      else
        echo "Verified ${platform}: $bin ($(wc -c < "$bin") bytes)"
      fi
    done
    if [ "$missing" -ne 0 ]; then
      exit 1
    fi
```

Then update the publish action's `binary` input to point at the directory:

```yaml
- name: Publish
  uses: brokenbots/publish-adapter@v0
  with:
    binary: ${{ github.workspace }}/artifact
    registry: ghcr.io/${{ github.repository_owner }}/<adapter-image-name>:${{ steps.ver.outputs.tag }}
```

### Step 3 — Add `artifact/` to `.gitignore`

```
artifact/
```

### Step 4 — Add a `make cross-build` target

```makefile
cross-build: ## Cross-build all supported platforms into artifact/bin/<os>/<arch>/
	@for platform in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64; do \
		os="$${platform%/*}"; arch="$${platform#*/}"; \
		outdir="artifact/bin/$${os}/$${arch}"; \
		mkdir -p "$$outdir"; \
		echo "building $${platform}"; \
		CGO_ENABLED=0 GOOS="$$os" GOARCH="$$arch" \
		  $(GO) build -trimpath -o "$$outdir/<adapter-binary-name>" .; \
	done
```

---

## How to Test the Fix

### Local verification

```sh
# 1. Run the cross-build target
make cross-build

# 2. Verify all four binaries were produced
ls -l artifact/bin/*/*/criteria-adapter-shell

# 3. Spot-check that each binary targets the right OS/arch
file artifact/bin/linux/amd64/criteria-adapter-shell    # → ELF 64-bit x86-64
file artifact/bin/linux/arm64/criteria-adapter-shell    # → ELF 64-bit aarch64
file artifact/bin/darwin/amd64/criteria-adapter-shell   # → Mach-O 64-bit x86_64
file artifact/bin/darwin/arm64/criteria-adapter-shell   # → Mach-O 64-bit arm64
```

### CI verification

1. Push a `v*` tag and confirm the publish workflow completes successfully.
2. In the workflow log, verify:
   - The "Cross-build adapter binaries" step prints `building …` for all four platforms.
   - The "Verify multi-platform build" step prints `Verified …` for all four platforms with non-zero byte counts.
   - The "Publish" step succeeds.

### Registry verification

After a successful publish:

```sh
# Inspect the OCI manifest to confirm all four platforms are listed
 crane manifest ghcr.io/brokenbots/criteria-adapter-shell:<tag> | jq '.manifests[].platform'
```

Expected output — four entries:

```json
{ "architecture": "amd64", "os": "linux" }
{ "architecture": "arm64", "os": "linux" }
{ "architecture": "amd64", "os": "darwin" }
{ "architecture": "arm64", "os": "darwin" }
```

### Pull verification (macOS / arm64 host)

On an arm64 macOS machine (or any platform that was previously failing):

```sh
criteria adapter pull ghcr.io/brokenbots/criteria-adapter-shell:<tag>
# Should resolve and pull the darwin/arm64 variant without "no matching platform" error
```

---

## Checklist for New Adapters

When extracting or creating a new adapter, verify each item before shipping:

- [ ] `Platforms` field in the Go `InfoResponse()` lists all four: `linux/amd64`, `linux/arm64`, `darwin/amd64`, `darwin/arm64`
- [ ] `publish.yml` uses the cross-compile loop pattern (not a single `go build`)
- [ ] `publish.yml` passes `artifact/` (directory) as the `binary` input, not a single binary path
- [ ] `publish.yml` includes a "Verify multi-platform build" step that asserts all four binaries exist
- [ ] `artifact/` is listed in `.gitignore`
- [ ] `Makefile` includes a `cross-build` target for local reproduction
- [ ] `crane manifest` on the published artifact shows all four platforms
- [ ] Adapter is pure Go (no CGO) — if it needs CGO, cross-compilation requires toolchains and this pattern does not apply as-is