package main

// timeout_ms_test.go — the shell adapter's honoring of ExecuteRequest.timeout_ms
// (KB-212 leg 3; wire field from criteria-adapter-proto v0.8.0 / KB-282). The
// resolver maps a non-zero declared timeout onto the validated hard timeout
// (1s–1h, applied as-is, no silent clamp), and 0 keeps the pre-v0.8.0 behavior
// byte-for-byte: the legacy `timeout` step input, else the 5m default.

import (
	"context"
	"encoding/json"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	v2 "github.com/brokenbots/criteria-adapter-proto/criteria/v2"
	adapterhost "github.com/brokenbots/criteria-go-adapter-sdk/adapterhost"
)

// ─── resolver mapping units (acceptance 1) ───────────────────────────────────

// TestResolveStepTimeout_DeclaredMapping pins the timeout_ms → hard-timeout
// mapping: 0 falls back to today's resolution byte-for-byte; a non-zero
// declaration wins over the legacy input and must land inside the 1s–1h window
// as-is, with out-of-range declarations failing and the error naming the
// declared value and the bound.
func TestResolveStepTimeout_DeclaredMapping(t *testing.T) {
	tests := []struct {
		name    string
		nms     int64
		input   string
		want    time.Duration
		wantErr []string // all substrings the error must carry; empty = no error expected
	}{
		{
			name:  "unset: falls back to the 5m default",
			nms:   0,
			input: "",
			want:  5 * time.Minute,
		},
		{
			name:  "unset: legacy input timeout still applies",
			nms:   0,
			input: "45s",
			want:  45 * time.Second,
		},
		{
			name:  "declared 60m honored as-is (exact max)",
			nms:   60 * 60 * 1000,
			input: "",
			want:  time.Hour,
		},
		{
			name:  "declared wins over legacy input timeout",
			nms:   3600000,
			input: "10m",
			want:  time.Hour,
		},
		{
			name:  "declared exactly 1s accepted",
			nms:   1000,
			input: "",
			want:  time.Second,
		},
		{
			name:    "declared 500ms below the 1s minimum errors",
			nms:     500,
			input:   "",
			wantErr: []string{"500ms", "timeout_ms=500", "1s–1h"},
		},
		{
			name:    "declared 999ms below the 1s minimum errors",
			nms:     999,
			input:   "",
			wantErr: []string{"999ms", "1s–1h"},
		},
		{
			name:    "negative declaration errors",
			nms:     -5000,
			input:   "",
			wantErr: []string{"-5s", "timeout_ms=-5000", "1s–1h"},
		},
		{
			name:    "declared 61m exceeds the 1h maximum errors",
			nms:     61 * 60 * 1000,
			input:   "",
			wantErr: []string{"1h1m0s", "timeout_ms=3660000", "1s–1h"},
		},
		{
			name:    "declared 3600001ms exceeds the 1h maximum errors",
			nms:     3600001,
			input:   "",
			wantErr: []string{"1h0m0.001s", "1s–1h"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveStepTimeout(tc.nms, tc.input)
			if len(tc.wantErr) == 0 {
				if err != nil {
					t.Fatalf("resolveStepTimeout(%d, %q): unexpected error %v", tc.nms, tc.input, err)
				}
				if got != tc.want {
					t.Fatalf("resolveStepTimeout(%d, %q) = %v, want %v", tc.nms, tc.input, got, tc.want)
				}
				return
			}
			if err == nil {
				t.Fatalf("resolveStepTimeout(%d, %q): expected error naming the bound, got %v", tc.nms, tc.input, got)
			}
			for _, sub := range tc.wantErr {
				if !strings.Contains(err.Error(), sub) {
					t.Errorf("error %q is missing %q", err.Error(), sub)
				}
			}
		})
	}
}

// TestSandbox_TimeoutMs_DefaultWhenUnset verifies the sandbox config still
// resolves the shipped 5m default when the host declared nothing (unset
// timeout_ms and no legacy input): "today's behavior byte-for-byte".
func TestSandbox_TimeoutMs_DefaultWhenUnset(t *testing.T) {
	cfg, err := buildSandboxConfig(map[string]string{"command": "true"}, nil, 0)
	if err != nil {
		t.Fatalf("buildSandboxConfig: %v", err)
	}
	if cfg.timeout != 5*time.Minute {
		t.Fatalf("cfg.timeout = %v, want the 5m default", cfg.timeout)
	}
}

// TestSandbox_TimeoutMs_PlumbsIntoConfig verifies buildSandboxConfig maps the
// declared milliseconds onto cfg.timeout (the hard kill window runShell uses),
// and that the legacy input path still reaches the same resolution.
func TestSandbox_TimeoutMs_PlumbsIntoConfig(t *testing.T) {
	for _, tc := range []struct {
		label        string
		declaredMs   int64
		timeoutInput string
		want         time.Duration
	}{
		{"declared 60m → 1h", 3600000, "", time.Hour},
		{"declared 1s", 1000, "", time.Second},
		{"legacy input only", 0, "90s", 90 * time.Second},
	} {
		t.Run(tc.label, func(t *testing.T) {
			input := map[string]string{"command": "true"}
			if tc.timeoutInput != "" {
				input["timeout"] = tc.timeoutInput
			}
			cfg, err := buildSandboxConfig(input, nil, tc.declaredMs)
			if err != nil {
				t.Fatalf("buildSandboxConfig: %v", err)
			}
			if cfg.timeout != tc.want {
				t.Fatalf("cfg.timeout = %v, want %v", cfg.timeout, tc.want)
			}
		})
	}
}

// ─── runShell behavioral tests ───────────────────────────────────────────────

// TestShell_DeclaredTimeout_Honored runs a stub step that outlives a scaled
// default: the declaration, not the default, governs the kill window. Uses the
// defaultTimeout test seam (see sandbox.go) to scale the shipped default.
func TestShell_DeclaredTimeout_Honored(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell adapter uses sh; skip on Windows")
	}
	scaleDefaultTimeout(t, 500*time.Millisecond)

	start := time.Now()
	outcome, outputs, events, err := runShell(
		context.Background(),
		map[string]string{"command": "sleep 2 && echo alive-past-default"},
		nil,
		60*60*1000, // declared 60m on the wire
		func(string, []byte) {},
	)
	if err != nil {
		t.Fatalf("runShell: %v", err)
	}
	if time.Since(start) > 60*time.Second {
		t.Fatalf("took too long: %v", time.Since(start))
	}
	if outcome != "success" {
		t.Fatalf("outcome = %q, want success (declared 60m must not be clamped to the default)", outcome)
	}
	if stdout, ok := outputs["stdout"].(string); !ok || !strings.Contains(stdout, "alive-past-default") {
		t.Fatalf("stdout output = %#v, want the stub echo proving survival past the default window", outputs["stdout"])
	}
	if _, found := findAdapterEvent(adapterEventMaps(events), "timeout"); found {
		t.Errorf("unexpected timeout event: %v", events)
	}
}

// TestShell_DeclaredTimeout_OutOfRangeErrors runs the two out-of-range
// acceptance cases through runShell: the step fails with a clear validation
// error naming the declared value and the 1s–1h bound (no silent clamp).
func TestShell_DeclaredTimeout_OutOfRangeErrors(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell adapter uses sh; skip on Windows")
	}
	for _, tc := range []struct {
		label    string
		ms       int64
		wantErrs []string
	}{
		{"500ms below minimum", 500, []string{"500ms", "timeout_ms=500", "1s–1h"}},
		{"61m above maximum", 61 * 60 * 1000, []string{"1h1m0s", "timeout_ms=3660000", "1s–1h"}},
	} {
		t.Run(tc.label, func(t *testing.T) {
			outcome, _, _, err := runShell(
				context.Background(),
				map[string]string{"command": "echo should-not-run"},
				nil,
				tc.ms,
				func(string, []byte) {},
			)
			if err == nil {
				t.Fatalf("outcome %q: expected a validation error naming the bound", outcome)
			}
			for _, sub := range tc.wantErrs {
				if !strings.Contains(err.Error(), sub) {
					t.Errorf("error %q is missing %q", err.Error(), sub)
				}
			}
		})
	}
}

// ─── host-level scenarios (acceptance 2) ─────────────────────────────────────

// logRecorder captures the adapter-side execution log: the stdout/stderr
// LogEvents the Service streams from the Execute emit path.
type logRecorder struct {
	mu     sync.Mutex
	logged []string
}

func (r *logRecorder) Send(ev *v2.LogEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logged = append(r.logged, string(ev.GetLine()))
	return nil
}

func (r *logRecorder) lines() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return strings.Join(r.logged, "")
}

// executeRecorder captures the Execute stream: adapter events and the result.
type executeRecorder struct {
	mu     sync.Mutex
	events []*v2.ExecuteEvent
}

func (r *executeRecorder) Send(ev *v2.ExecuteEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
	return nil
}

// scaleDefaultTimeout shrinks the shipped 5m default for a test and restores
// it at cleanup; the resolver's declared path bypasses it entirely.
func scaleDefaultTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	prev := defaultTimeout
	t.Cleanup(func() { defaultTimeout = prev })
	defaultTimeout = d
}

// waitForRegisteredLogSender spins until the Service has registered the log
// sender for sid (Log runs on its own goroutine, as the host's stream open
// does), or fails the test after 2s.
func waitForRegisteredLogSender(t *testing.T, svc *Service, sid string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		registered := false
		svc.mu.Lock()
		sess := svc.sessions[sid]
		svc.mu.Unlock()
		if sess != nil {
			sess.logMu.Lock()
			registered = sess.sender != nil
			sess.logMu.Unlock()
		}
		if registered {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("host-level scenario: Log sender never registered")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// hostLevelExecute drives Service.Execute the way the engine does: OpenSession,
// open the Log stream, then Execute with TimeoutMs populated on the v2 request
// and a sink for Execute events. It returns the captured adapter-side log
// text, the Execute outcome, and its adapter events.
func hostLevelExecute(t *testing.T, ms int64, command string) (logText string, outcome string, adapterEvents []map[string]any) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell adapter uses sh; skip on Windows")
	}
	svc := NewService()
	sid := t.Name()

	logCtx, cancelLog := context.WithCancel(context.Background())
	t.Cleanup(cancelLog)
	logRec := &logRecorder{}
	go func() { _ = svc.Log(logCtx, &v2.LogRequest{SessionId: sid}, logRec) }()

	if _, err := svc.OpenSession(context.Background(), &v2.OpenSessionRequest{
		SessionId: sid,
		Config:    map[string]string{},
		Secrets:   map[string]string{},
	}); err != nil {
		t.Fatalf("OpenSession: %v", err)
	}
	waitForRegisteredLogSender(t, svc, sid)

	rec := &executeRecorder{}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := svc.Execute(ctx, &v2.ExecuteRequest{
		SessionId: sid,
		StepName:  "declared_timeout_scenario",
		Input:     map[string]string{"command": command},
		TimeoutMs: ms,
	}, rec); err != nil {
		t.Fatalf("Execute(declared timeout_ms=%d): %v", ms, err)
	}
	outcome, _ = rec.result(t)
	return logRec.lines(), outcome, rec.adapterEvents()
}

// TestHostLevel_DeclaredTimeout_CutsOffLongStub proves, end to end through
// Service.Execute, that a declared 3s kills a step that would otherwise live
// for minutes under the untouched 5m default: the declaration, not the
// default, drives the kill. The adapter-side timeout event names the applied
// limit so the execution log carries the evidence.
func TestHostLevel_DeclaredTimeout_CutsOffLongStub(t *testing.T) {
	start := time.Now()
	logText, outcome, adapterEvents := hostLevelExecute(t, 3_000, "sleep 3000")
	elapsed := time.Since(start)

	if elapsed > 90*time.Second {
		t.Fatalf("step was not cut off by the declaration: elapsed %v (a 5m-default kill takes 5m+), log=%q", elapsed, logText)
	}
	if outcome != "failure" {
		t.Fatalf("outcome = %q, want failure (declared 3s must kill the sleep)", outcome)
	}
	timeoutEvent, found := findAdapterEvent(adapterEvents, "timeout")
	if !found {
		t.Fatalf("no timeout event on the Execute stream (adapter events: %v)", adapterEvents)
	}
	if limit := timeoutEvent["limit"]; limit != "3s" {
		t.Errorf("timeout event limit = %v, want the declared 3s (not the 5m default)", limit)
	}
}

// TestHostLevel_DeclaredTimeout_SurvivesPastDefaultWindow proves a declared
// 60m step survives past the 5m default without being killed: with the default
// scaled to 500ms, a `sleep 2 && echo survivor` stub would die under the
// pre-fix behavior (default honored over the declaration) but must succeed
// here, with its echo visible in the adapter-side execution log.
func TestHostLevel_DeclaredTimeout_SurvivesPastDefaultWindow(t *testing.T) {
	scaleDefaultTimeout(t, 500*time.Millisecond)

	logText, outcome, adapterEvents := hostLevelExecute(t, 60*60*1000, "sleep 2 && echo survivor")
	if outcome != "success" {
		t.Fatalf("outcome = %q, want success (declared 60m must survive past the default window; events=%v)", outcome, adapterEvents)
	}
	if !strings.Contains(logText, "survivor") {
		t.Fatalf("adapter-side execution log lost the stub echo; got %q", logText)
	}
	if _, found := findAdapterEvent(adapterEvents, "timeout"); found {
		t.Errorf("unexpected timeout event for a step inside its declared window: %v", adapterEvents)
	}
}

// TestHostLevel_DeclaredTimeout_OutOfRangeFailsStep drives the out-of-range
// acceptance cases through Service.Execute: the host's declared timeout_ms out
// of the 1s–1h window fails the step with a clear validation error naming the
// declared value and the bound — no silent clamp, at the wire boundary.
func TestHostLevel_DeclaredTimeout_OutOfRangeFailsStep(t *testing.T) {
	for _, tc := range []struct {
		label    string
		ms       int64
		wantErrs []string
	}{
		{"declared 500ms rejected", 500, []string{"500ms", "timeout_ms=500", "1s–1h"}},
		{"declared 61m rejected", 61 * 60 * 1000, []string{"1h1m0s", "timeout_ms=3660000", "1s–1h"}},
	} {
		t.Run(tc.label, func(t *testing.T) {
			if runtime.GOOS == "windows" {
				t.Skip("shell adapter uses sh; skip on Windows")
			}
			svc := NewService()
			sid := tc.label
			if _, err := svc.OpenSession(context.Background(), &v2.OpenSessionRequest{
				SessionId: sid, Config: map[string]string{}, Secrets: map[string]string{},
			}); err != nil {
				t.Fatalf("OpenSession: %v", err)
			}
			err := svc.Execute(context.Background(), &v2.ExecuteRequest{
				SessionId: sid,
				StepName:  "out_of_range_gate",
				Input:     map[string]string{"command": "should-not-run"},
				TimeoutMs: tc.ms,
			}, &executeRecorder{})
			if err == nil {
				t.Fatalf("Execute(timeout_ms=%d): expected error naming the bound", tc.ms)
			}
			for _, sub := range tc.wantErrs {
				if !strings.Contains(err.Error(), sub) {
					t.Errorf("error %q is missing %q", err.Error(), sub)
				}
			}
		})
	}
}

// ─── shared helpers ──────────────────────────────────────────────────────────

// adapterEventMaps flattens adapterEvt payloads for assertions.
func adapterEventMaps(events []adapterEvt) []map[string]any {
	var out []map[string]any
	for _, ev := range events {
		out = append(out, ev.payload)
	}
	return out
}

// findAdapterEvent returns the first adapter event whose event_type matches.
func findAdapterEvent(events []map[string]any, eventType string) (map[string]any, bool) {
	for _, ev := range events {
		if ev["event_type"] == eventType {
			return ev, true
		}
	}
	return nil, false
}

// result returns the terminal ExecuteResult outcome and decoded outputs.
func (r *executeRecorder) result(t *testing.T) (string, map[string]any) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	var result *v2.ExecuteResult
	for _, ev := range r.events {
		if ev.GetResult() != nil {
			result = ev.GetResult()
		}
	}
	if result == nil {
		t.Fatalf("Execute stream produced no result event (events seen: %d)", len(r.events))
	}
	var outputs map[string]any
	if len(result.GetOutputsJson()) > 0 {
		if err := json.Unmarshal(result.GetOutputsJson(), &outputs); err != nil {
			t.Fatalf("unmarshal outputs json %s: %v", result.GetOutputsJson(), err)
		}
	}
	return result.GetOutcome(), outputs
}

// adapterEvents flattens the captured stream to the adapter events' payloads.
func (r *executeRecorder) adapterEvents() []map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []map[string]any
	for _, ev := range r.events {
		if a := ev.GetAdapter(); a != nil && a.GetPayload() != nil {
			out = append(out, a.GetPayload().AsMap())
		}
	}
	return out
}

// keep the sink/sender contracts honest: these must satisfy the SDK interfaces.
var _ adapterhost.ExecuteEventSender = (*executeRecorder)(nil)
var _ adapterhost.LogEventSender = (*logRecorder)(nil)