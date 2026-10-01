package main

// lifecycle_test.go — pause-gate and stop/resume-re-exec tests for the shell
// adapter (CRI-205). The pause contract asserted here mirrors the engine's
// conformance pause_resume suite: the pause acks only at a command boundary;
// an Execute issued while paused stalls until Resume (or fails on a bounded
// pause wait); after resume, Executes run again. The re-exec tests assert the
// state-mode-"none" story: a session opened after a stop runs against the
// SURVIVING worktree with no duplicated work and nothing clobbered at open.

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	v2 "github.com/brokenbots/criteria-adapter-proto/criteria/v2"
)

// nopEventSink discards Execute-stream events; the Service tests care about
// outcomes and lifecycle ordering, not wire framing.
type nopEventSink struct{}

func (nopEventSink) Send(*v2.ExecuteEvent) error { return nil }

func openSession(t *testing.T, s *Service, id string) {
	t.Helper()
	if _, err := s.OpenSession(context.Background(), &v2.OpenSessionRequest{SessionId: id}); err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
}

func pauseReq(id string) *v2.PauseRequest {
	return &v2.PauseRequest{SessionId: id}
}

func resumeReq(id string) *v2.ResumeRequest {
	return &v2.ResumeRequest{SessionId: id}
}

func closeReq(id string) *v2.CloseSessionRequest {
	return &v2.CloseSessionRequest{SessionId: id}
}

func executeReq(sessionID string, input map[string]string) *v2.ExecuteRequest {
	return &v2.ExecuteRequest{SessionId: sessionID, StepName: "step", Input: input}
}

// execStep runs one step through the Service (pause gate included),
// discarding the Execute stream. The Service only reports a Go error — the
// outcome itself travels on the event stream, which this test discards.
func execStep(t *testing.T, s *Service, sessionID string, input map[string]string) error {
	return execStepCtx(t, context.Background(), s, sessionID, input)
}

func execStepCtx(t *testing.T, ctx context.Context, s *Service, sessionID string, input map[string]string) error {
	t.Helper()
	return s.Execute(ctx, executeReq(sessionID, input), nopEventSink{})
}

func TestInfo_DeclaresStateModeNone(t *testing.T) {
	info := InfoResponse()
	st := info.GetState()
	if st == nil {
		t.Fatal("InfoResponse.state is nil; want StateDescriptor{mode: none}")
	}
	if st.GetMode() != "none" {
		// Absent and "none" mean the same thing on the wire, but the shell
		// declaration must be explicit so the host sees intent.
		t.Errorf("state.mode = %q, want %q", st.GetMode(), "none")
	}
	if st.GetSchema() != "" || st.GetMaxBytes() != 0 || st.GetGranularity() != "" {
		t.Errorf("mode %q must not require schema/max_bytes/granularity; got %+v", st.GetMode(), st)
	}
}

// TestLifecycle_PauseWaitsForCommandBoundary pins the boundary rule at the
// gate level, deterministically: Pause called while a command is mid-flight
// does not ack (and never kills anything — there is no kill path in the
// gate); when the command reaches its boundary the ack lands. It also pins
// that a cancelled pause leaves the session runnable (no half-applied gate).
func TestLifecycle_PauseWaitsForCommandBoundary(t *testing.T) {
	g := newLifecycleGate()

	// Simulate a command in flight.
	release, started := gateEnter(g)
	if !started {
		t.Fatal("enter on a fresh gate must be immediate")
	}

	// Mid-command pause: bounded wait must NOT ack while in flight.
	pctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	err := g.Pause(pctx)
	cancel()
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Pause acked mid-command: err=%v (want deadline exceeded)", err)
	}

	// The command drains to its boundary: no partial-command kill signal
	// exists — release only signals the gate, never cancels a child.
	release()

	// Now the pause acks promptly (drained).
	actx, acancel := context.WithTimeout(context.Background(), time.Second)
	defer acancel()
	if err := g.Pause(actx); err != nil {
		t.Fatalf("Pause after drain: %v", err)
	}

	// Held Executes stall while paused; Resume releases them.
	go func() {
		time.Sleep(50 * time.Millisecond)
		g.Resume()
	}()
	ectx, ecancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer ecancel()
	if _, err := g.enter(ectx, "s"); err != nil {
		t.Fatalf("enter after Pause+Resume: %v", err)
	}
}

// TestLifecycle_PauseAckBlocksNewExecutes deterministically: once the pause
// ack lands (no in-flight command), a new Execute stalls; cancellation of the
// Execute's own ctx unblocks with the typed error, and the gate stays closed
// for new work until Resume.
func TestLifecycle_PauseAckBlocksNewExecutes(t *testing.T) {
	g := newLifecycleGate()
	if err := g.Pause(context.Background()); err != nil {
		t.Fatalf("Pause on idle gate: %v", err)
	}
	ectx, ecancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer ecancel()
	if _, err := g.enter(ectx, "s"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("enter while paused: err=%v (want deadline exceeded)", err)
	}
	g.Resume()
	if _, err := g.enter(context.Background(), "s"); err != nil {
		t.Fatalf("enter after Resume: %v", err)
	}
}

func TestLifecycle_PauseIdempotent_ResumeIdempotent(t *testing.T) {
	s := NewService()
	openSession(t, s, "sess-idem")

	if _, err := s.Pause(context.Background(), pauseReq("sess-idem")); err != nil {
		t.Fatalf("first Pause: %v", err)
	}
	// Pausing twice must be a no-op (host contract: idempotent), not stuck.
	pctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := s.Pause(pctx, pauseReq("sess-idem")); err != nil {
		t.Fatalf("second Pause: %v", err)
	}
	if _, err := s.Resume(context.Background(), resumeReq("sess-idem")); err != nil {
		t.Fatalf("first Resume: %v", err)
	}
	// Resuming an active session is a no-op, not an error or panic.
	if _, err := s.Resume(context.Background(), resumeReq("sess-idem")); err != nil {
		t.Fatalf("second Resume: %v", err)
	}
	if err := execStep(t, s, "sess-idem", map[string]string{"command": "true"}); err != nil {
		t.Fatalf("post-resume run: %v", err)
	}
}

// TestLifecycle_PauseMidCommandDrainsChild is the integration twin of the
// gate-boundary test: a long-running command straddles a pause; the child is
// not killed (its full body completes), the pause acks after the boundary,
// and the worktree mutation survives. An Execute issued while the session is
// paused stalls, and the Resume path runs again.
func TestLifecycle_PauseMidCommandDrainsChild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell adapter uses sh; skip on Windows")
	}
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("CRITERIA_SHELL_ALLOWED_PATHS", dir)
	marker := filepath.Join(dir, "marker.txt")

	s := NewService()
	openSession(t, s, "sess-1")

	input := map[string]string{
		"command":           "touch " + marker + "; sleep 1.5; printf done > " + marker,
		"working_directory": dir,
	}
	execDone := make(chan struct{})
	var execErr error
	go func() {
		defer close(execDone)
		execErr = execStep(t, s, "sess-1", input)
	}()

	// Mid-flight pause with a generous ctx: the only thing that may hold the
	// ack is the in-flight command draining to its boundary.
	time.Sleep(300 * time.Millisecond)
	pauseCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t0 := time.Now()
	if _, err := s.Pause(pauseCtx, pauseReq("sess-1")); err != nil {
		cancel()
		t.Fatalf("Pause: %v", err)
	}
	cancel()
	if ackDelay := time.Since(t0); ackDelay > 10*time.Second {
		t.Errorf("pause ack overdue: %v", ackDelay)
	}

	<-execDone
	if execErr != nil {
		t.Fatalf("in-flight Execute: %v", execErr)
	}
	if data, err := os.ReadFile(marker); err != nil || string(data) != "done" {
		t.Fatalf("child did not drain to its boundary: marker=%q (err %v)", data, err)
	}

	// While paused, a bounded Execute stalls.
	runCtx, runCancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer runCancel()
	if err := execStepCtx(t, runCtx, s, "sess-1", map[string]string{"command": "true"}); err == nil {
		t.Fatal("expected an Execute issued while paused to stall on its bounded wait")
	}
	if runCtx.Err() == nil {
		t.Error("expected the Execute's own bounded wait to expire")
	}

	// Resume unblocks the gate; Execute works again afterwards.
	if _, err := s.Resume(context.Background(), resumeReq("sess-1")); err != nil {
		t.Fatalf("Resume: %v", err)
	}
	if err := execStep(t, s, "sess-1", map[string]string{"command": "printf resumed"}); err != nil {
		t.Fatalf("post-resume Execute: %v", err)
	}
}

// TestLifecycle_ReopenAfterStop_ContinuesWorktree_NoDuplication drives the
// stop/resume re-exec criterion: the adapter process (and its Service) dies at
// the stop; the worktree survives; the re-exec'd adapter re-opens a fresh
// session in the SAME worktree, does not duplicate the completed step's
// work, and continues from the surviving diff.
func TestLifecycle_ReopenAfterStop_ContinuesWorktree_NoDuplication(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell adapter uses sh; skip on Windows")
	}
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("CRITERIA_SHELL_ALLOWED_PATHS", dir)
	log := filepath.Join(dir, "ledger.txt")

	// Live phase: one completed step appends to the worktree.
	s := NewService()
	openSession(t, s, "sess-live")
	if err := execStep(t, s, "sess-live", map[string]string{
		"command":           "printf 'step1\\n' >> " + log,
		"working_directory": dir,
	}); err != nil {
		t.Fatalf("live step: %v", err)
	}
	if _, err := s.CloseSession(context.Background(), closeReq("sess-live")); err != nil {
		t.Fatalf("CloseSession: %v", err)
	}

	// Stop: the adapter process dies with it. The worktree on the PVC
	// survives; the fresh adapter re-executes against it.
	before, err := os.Stat(log)
	if err != nil {
		t.Fatalf("worktree did not survive the stop: %v", err)
	}
	resumed := NewService()
	openSession(t, resumed, "sess-2")

	// The engine re-issues only INCOMPLETE steps; a completed step's effect
	// is not replayed by the adapter. The resumed command asserts the live
	// worktree diff itself (ledger has exactly one line) before appending.
	if err := execStep(t, resumed, "sess-2", map[string]string{
		"command":           "test \"$(wc -l < " + log + ")\" -eq 1 && printf 'step2\\n' >> " + log,
		"working_directory": dir,
	}); err != nil {
		t.Fatalf("resumed step: %v", err)
	}
	after, err := os.Stat(log)
	if err != nil {
		t.Fatalf("stat ledger: %v", err)
	}
	if after.Size() <= before.Size() {
		t.Fatalf("no worktree diff after the resumed step: before=%d after=%d", before.Size(), after.Size())
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "step1\nstep2\n" {
		t.Fatalf("worktree diff wrong after re-exec: %q (want step1/step2)\n", string(data))
	}
}

func TestLifecycle_CloseReleasesPausedWaiters(t *testing.T) {
	s := NewService()
	openSession(t, s, "sess-close")
	if _, err := s.Pause(context.Background(), pauseReq("sess-close")); err != nil {
		t.Fatalf("Pause: %v", err)
	}

	execDone := make(chan error, 1)
	go func() {
		execDone <- execStep(t, s, "sess-close", map[string]string{"command": "true"})
	}()

	// Let the Execute reach the gate, then close the session: the waiter must
	// fail fast instead of waiting forever.
	time.Sleep(50 * time.Millisecond)
	if _, err := s.CloseSession(context.Background(), closeReq("sess-close")); err != nil {
		t.Fatalf("CloseSession: %v", err)
	}
	select {
	case err := <-execDone:
		if !errors.Is(err, errSessionClosed) {
			t.Fatalf("released Execute error = %v, want errSessionClosed wrap", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Execute did not fail fast after CloseSession; the gate leaked the waiter")
	}
}

func TestLifecycle_UnknownSessionsRefused(t *testing.T) {
	s := NewService()
	if _, err := s.Pause(context.Background(), pauseReq("nope")); err == nil {
		t.Error("Pause on unknown session must be refused")
	}
	if _, err := s.Resume(context.Background(), resumeReq("nope")); err == nil {
		t.Error("Resume on unknown session must be refused")
	}
	// Execute on an unknown session keeps its "unknown session" error.
	if err := execStep(t, s, "nope", map[string]string{"command": "true"}); err == nil {
		t.Error("Execute on unknown session must be refused")
	}
}

// gateEnter acquires a fake in-flight slot whose release only hits the gate
// (no child process involved).
func gateEnter(g *lifecycleGate) (release func(), started bool) {
	r, err := g.enter(context.Background(), "s")
	if err != nil {
		return nil, false
	}
	return r, true
}
