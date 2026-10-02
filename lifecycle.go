package main

// lifecycle.go — the shell adapter's pause gate.
//
// A command is the shell adapter's checkpoint granularity: a pause that
// arrives while a command is mid-flight WAITS for that command to reach its
// boundary instead of killing it. The running child drains naturally — no
// partial-command kills — and the pause ack is not returned until the session
// has no command in flight. A new Execute is held at the gate while the
// session is paused and is released by Resume (or fails fast on caller
// cancellation or session close).
//
// The pause/resume semantics are implemented adapter-side and are asserted by
// the repo's own tests. The wire transport that can deliver Pause/Resume to a
// Go adapter does not exist yet (the SDK's gRPC bridge covers only the six
// Service methods — see its service.go doc comment), so
// InfoResponse.supported_features stays empty until upstream lands the
// bridge; these methods are ready to be wired the moment it does.
//
// Stop/resume: the shell adapter declares state mode "none" (CRI-205) — the
// worktree IS its environment, not engine-managed checkpointable state. After
// a stop the host re-spawns the adapter and re-opens the session, and the
// adapter re-executes against the SURVIVING worktree: OpenSession only
// records config/secrets and never touches the filesystem, and Execute keeps
// working under the same per-step working-directory confinement, so the
// surviving worktree diff is continued with no adapter-side replay.
//
// Verified for CRI-210 (per-scope teardown): the adapter binary performs no
// durable filesystem I/O — only the spawned shell children write, and only
// under the per-step working_directory confinement (sandbox.go) — and all
// adapter state (sessions, gates) is process-local memory. Per-scope
// teardown therefore destroys only the pod, not anything the restore path
// needs: the PVC-resident worktree plus idempotent setup fully reproduce it.

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// lifecycleGate serialises pause state per session.
//
// Locking model: one mutex guards paused/closed/inflight, while
// drainedCh signals "no command in flight" to a waiting Pause and resumedCh
// wakes Executes held at the gate. Both channels are snapshot-then-waited: a
// waiter takes the channel pointer under mu and releases the lock before
// blocking, so state transitions never deadlock against their waiters.
type lifecycleGate struct {
	mu       sync.Mutex
	paused   bool
	closed   bool
	inflight int

	// resumedCh wakes Executes held at the gate. Resume closes the installed
	// channel and immediately installs a fresh one, so a spent channel never
	// lingers: Close (first Close only) then always closes an open channel,
	// releasing waiters with errSessionClosed.
	resumedCh chan struct{}
	// drainedCh is closed when inflight drains back to 0. It is created by
	// the Execute that transitions inflight 0→1 and closed by the one that
	// drains it back to 0, so a Pause that arrived mid-command waits on the
	// window's channel without polling.
	drainedCh chan struct{}
}

func newLifecycleGate() *lifecycleGate {
	return &lifecycleGate{
		resumedCh: make(chan struct{}),
		drainedCh: make(chan struct{}),
	}
}

// errSessionClosed is returned when an operation is refused because the
// session was closed while the caller was waiting on it.
var errSessionClosed = errors.New("shell adapter: session closed")

// Pause marks the session paused. If a command is mid-flight it waits for the
// current command's boundary (ctx-bounded) rather than interrupting it, and
// only then acks the pause — no partial-command kills.
//
// Pausing an already-paused session is a no-op (nil), mirroring the host's
// idempotency contract. A ctx cancellation or deadline expiry returns the ctx
// error and leaves the session unpaused: the pause did not take effect, so
// the next Execute must not be wedged behind a half-applied gate.
func (g *lifecycleGate) Pause(ctx context.Context) error {
	for {
		g.mu.Lock()
		if g.closed {
			g.mu.Unlock()
			return errSessionClosed
		}
		if g.paused {
			g.mu.Unlock()
			return nil
		}
		if g.inflight == 0 {
			g.paused = true
			g.resumedCh = make(chan struct{})
			g.mu.Unlock()
			return nil
		}
		// Mid-command pause: wait for the command's boundary, then re-check.
		drainedCh := g.drainedCh
		g.mu.Unlock()
		select {
		case <-drainedCh:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// Resume lifts the pause, releasing any Execute held at the gate. Close the
// spent channel FIRST (waking the waiters blocked on it), then install a
// fresh open one — installing before closing would strand those waiters, and
// leaving the spent channel installed would make a later Close panic on a
// "close of closed channel" after a completed pause/resume cycle. Resuming an
// already-active session is a no-op; a closed gate stays closed.
func (g *lifecycleGate) Resume() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed || !g.paused {
		return
	}
	g.paused = false
	close(g.resumedCh)
	g.resumedCh = make(chan struct{})
}

// Close tears the gate down, releasing the waiters. Only the first Close
// runs: it closes the resumedCh that current waiters hold, releasing every
// blocked Execute with errSessionClosed. (A replaced-then-closed scheme would
// strand waiters that are already blocked on the older channel.) Because both
// Pause and Resume install a fresh channel whenever they close a spent one,
// the channel installed at Close time is always open — the double Close is
// still guarded by g.closed.
func (g *lifecycleGate) Close() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.closed {
		return
	}
	g.closed = true
	close(g.resumedCh)
}

// enter blocks until the session is runnable (not paused, still open) or the
// caller gives up (ctx cancellation / deadline). On success it registers the
// caller as in flight; the returned release func must be called when the
// command reaches its boundary — it closes drainedCh when the count drains to
// zero so a pending Pause acks.
func (g *lifecycleGate) enter(ctx context.Context, sessionID string) (release func(), err error) {
	for {
		g.mu.Lock()
		if g.closed {
			g.mu.Unlock()
			return nil, errSessionClosed
		}
		if !g.paused {
			if g.inflight == 0 {
				// Fresh in-flight window: a new drained signal for a pending
				// Pause to wait on.
				g.drainedCh = make(chan struct{})
			}
			g.inflight++
			g.mu.Unlock()
			return g.releaseBoundary, nil
		}
		resumedCh := g.resumedCh
		g.mu.Unlock()
		select {
		case <-resumedCh:
		case <-ctx.Done():
			return nil, fmt.Errorf("shell adapter: session %q is paused: %w", sessionID, ctx.Err())
		}
	}
}

// releaseBoundary runs on the Execute path whenever a command reaches its
// boundary, whether the command succeeded, failed, or errored.
func (g *lifecycleGate) releaseBoundary() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.inflight--
	// drainedCh belongs to this in-flight window alone: it is created by the
	// 0→1 transition in enter and replaced only by the next one, which cannot
	// happen before this drain reaches 0, so this close runs exactly once.
	if g.inflight == 0 {
		close(g.drainedCh)
	}
}
