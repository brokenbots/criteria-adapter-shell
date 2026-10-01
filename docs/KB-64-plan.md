# KB-64 / CRI-205 implementation plan — shell adapter state none + pause-safe idle + worktree re-exec

Branch: KB-64 (base main 0799cb9). Working dir: /data/intake/KB-64/worktree.

## Verified engine/SDK contract (context for design)
- SDK v0.5.4 (latest) `adapterhost/service.go`: lifecycle RPCs (Pause/Resume/Snapshot/Restore/Inspect)
  are explicitly NOT bridged — `grpcAdapterServer` answers them from the generated
  Unimplemented stubs. No injection point. Verified against SDK main as well.
- Engine (brokenbots/criteria): explicit pause calls `Session.Pause` → `PauseAll` → Pause RPC
  (unconditional, NOT gated on supported_features), then `SnapshotAll` (mode-ungated too).
  Boundary pause fails open with a warning. Engine conformance `pause_resume` suite defines
  the adapter-side contract: pause → nil; Execute while paused fails-or-stalls; resume → nil;
  Execute works again. `testPauseResume` is skipped unless adapter declares pause+resume
  in supported_features.
- proto v0.6.0: `InfoResponse.state` (field 18) `StateDescriptor{mode,schema,max_bytes,granularity}`;
  mode "none" = identical to absent descriptor — "a fresh start on every (re)spawn"; schema/max_bytes
 /granularity required only for blob/ref. No typed constants — bare string "none".
- Engine pause does NOT cancel in-flight Execute RPCs (drain-first). The shell child can therefore
  only be killed by step timeout, host teardown, or process death — the adapter's own obligation is
  to never kill a child in any pause-adjacent path and to treat the command as checkpoint granularity.

## Implementation mapping
1. **State mode none** — go.mod proto v0.5.1→v0.6.0 (+ tidy); shell.go InfoResponse():
   `State: &v2.StateDescriptor{Mode: "none"}` with worktree-is-environment comment.
2. **Pause-safe idle** — new lifecycle.go: per-session pause gate inside the shell Service
   (Pause marks session paused ONLY after in-flight command drains — waits for the current
   command's boundary, never kills a child; Execute blocks at the gate while paused, stalls on
   pause-ctx; Resume unblocks idempotently). Wire Pause/Resume as Service methods so the day the
   SDK bridges lifecycle RPCs the shell adapter is ready with no adapter-side change. CloseSession
   releases gate waiters. supported_features stays EMPTY: advertising pause/resume today would
   promise an RPC the SDK transport cannot deliver (engine would hit Unimplemented).
3. **Stop/resume re-exec** — mode-none semantics: the adapter re-executes against the SURVIVING
   worktree on any fresh open. OpenSession records config/secrets only (never touches the FS);
   Execute runs under the same per-step working-directory confinement, so a re-opened session's
   steps land in the surviving diff. No adapter-side replay/cache exists, so no duplicated work
   is possible from the adapter; the engine's step ledger governs which steps are re-issued.
   Covered by tests (fresh-Service + fresh-session in the same worktree; diff preserved; no dup).
4. **Teardown note for CRI-210** — verify adapter touches no durable state outside the step
   working_directory (grep for file I/O; sessionState is process memory only), so pod-level
   teardown of per-scope adapters is safe; worktree survives on the data PVC. Record in PR + reason.

## Tests
- TestInfo_DeclaresStateModeNone (declaration + none needs no other fields).
- TestLifecycle_PauseMidCommandDrainsBoundary (long command straddles pause; ack after drain;
  child not killed — marker written; worktree intact).
- TestLifecycle_ExecuteStallsWhilePaused / TestLifecycle_ResumeReleases.
- TestLifecycle_CloseReleasesPausedWaiters.
- TestLifecycle_ReopenAgainstWorktreeNoDuplication (stop/resume re-exec: fresh service + session
  in same worktree; diff preserved; no duplicated append).

## Gates
make build && make vet && make test && make tidy && make vuln-scan (local toolchain /tmp/gotool/go).