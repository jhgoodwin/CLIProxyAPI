package auth

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestApplyRequestAfterAuthInterceptorUsesSelectedAuthPolicyContext(t *testing.T) {
	var got cliproxyexecutor.RequestAfterAuthInterceptRequest
	selected := &Auth{ID: "selected-account", Provider: "claude"}
	opts := cliproxyexecutor.Options{
		ProviderPoolAuthIDs: []string{"account-a", "account-b"},
		RequestAfterAuthInterceptor: func(_ context.Context, req cliproxyexecutor.RequestAfterAuthInterceptRequest) cliproxyexecutor.RequestAfterAuthInterceptResponse {
			got = req
			return cliproxyexecutor.RequestAfterAuthInterceptResponse{}
		},
	}
	ctx := cliproxyexecutor.WithAttemptID(context.Background(), "attempt-123")

	_, _, err := applyRequestAfterAuthInterceptor(ctx, nil, "openai", cliproxyexecutor.Request{}, opts, "model", selected)
	if err != nil {
		t.Fatalf("applyRequestAfterAuthInterceptor() error = %v", err)
	}
	if got.Provider != "claude" || got.AuthID != "selected-account" || got.AttemptID != "attempt-123" {
		t.Fatalf("policy identity = provider %q, auth %q, attempt %q; want claude, selected-account, attempt-123", got.Provider, got.AuthID, got.AttemptID)
	}
	if !reflect.DeepEqual(got.ProviderPoolAuthIDs, []string{"account-a", "account-b"}) {
		t.Fatalf("ProviderPoolAuthIDs = %v", got.ProviderPoolAuthIDs)
	}
	// The callback receives its own snapshot and cannot mutate the execution options.
	got.ProviderPoolAuthIDs[0] = "mutated"
	if opts.ProviderPoolAuthIDs[0] != "account-a" {
		t.Fatalf("interceptor mutated execution options: %v", opts.ProviderPoolAuthIDs)
	}
}

func TestProviderPoolAuthIDsIncludesTemporarilyUnavailableAccounts(t *testing.T) {
	m := NewManager(nil, nil, nil)
	for _, auth := range []*Auth{
		{ID: "ready", Provider: "claude"},
		{ID: "cooldown", Provider: "claude", Unavailable: true},
		{ID: "disabled-flag", Provider: "claude", Disabled: true},
		{ID: "disabled-status", Provider: "claude", Status: StatusDisabled},
		{ID: "other-provider-disabled", Provider: "openai", Status: StatusDisabled},
	} {
		if _, err := m.Register(context.Background(), auth); err != nil {
			t.Fatalf("Register(%q): %v", auth.ID, err)
		}
	}

	got := m.providerPoolAuthIDs("CLAUDE")
	want := []string{"cooldown", "ready"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("providerPoolAuthIDs() = %v, want %v", got, want)
	}
}

type policyCountRefreshExecutor struct {
	*unauthorizedRefreshExecutor
	calls []string
}

func (e *policyCountRefreshExecutor) CountTokens(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.calls = append(e.calls, auth.ID)
	if authAccessToken(auth) == "stale-access-token" {
		return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusUnauthorized, Message: "unauthorized"}
	}
	return cliproxyexecutor.Response{Payload: []byte("counted")}, nil
}

type policyBootstrapRefreshExecutor struct {
	*unauthorizedRefreshExecutor
}

func (e *policyBootstrapRefreshExecutor) ExecuteStream(ctx context.Context, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	stream, err := e.unauthorizedRefreshExecutor.ExecuteStream(ctx, auth, req, opts)
	if err == nil {
		return stream, nil
	}
	chunks := make(chan cliproxyexecutor.StreamChunk, 1)
	chunks <- cliproxyexecutor.StreamChunk{Err: err}
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func TestManager_RequiredRequestPolicyReauthorizesRefreshedRetry(t *testing.T) {
	tests := []struct {
		name  string
		run   func(*Manager, string, cliproxyexecutor.Options) error
		calls func(*unauthorizedRefreshExecutor) []string
	}{
		{
			name: "execute",
			run: func(m *Manager, model string, opts cliproxyexecutor.Options) error {
				_, err := m.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, opts)
				return err
			},
			calls: (*unauthorizedRefreshExecutor).ExecuteCalls,
		},
		{
			name: "stream",
			run: func(m *Manager, model string, opts cliproxyexecutor.Options) error {
				_, err := m.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, opts)
				return err
			},
			calls: (*unauthorizedRefreshExecutor).StreamCalls,
		},
		{
			name: "stream bootstrap",
			run: func(m *Manager, model string, opts cliproxyexecutor.Options) error {
				_, err := m.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, opts)
				return err
			},
			calls: (*unauthorizedRefreshExecutor).StreamCalls,
		},
		{
			name: "count tokens",
			run: func(m *Manager, model string, opts cliproxyexecutor.Options) error {
				_, err := m.ExecuteCount(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, opts)
				return err
			},
			calls: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, executor, primary, _, model := newUnauthorizedRefreshFixture(t, false)
			var attemptIDs []string
			opts := cliproxyexecutor.Options{
				RequestAfterAuthInterceptorRequired: true,
				RequestAfterAuthInterceptor: func(_ context.Context, req cliproxyexecutor.RequestAfterAuthInterceptRequest) cliproxyexecutor.RequestAfterAuthInterceptResponse {
					attemptIDs = append(attemptIDs, req.AttemptID)
					if len(attemptIDs) == 2 {
						return cliproxyexecutor.RequestAfterAuthInterceptResponse{Terminate: true, StatusCode: http.StatusForbidden, ResponseBody: []byte("policy denied")}
					}
					return cliproxyexecutor.RequestAfterAuthInterceptResponse{}
				},
			}
			if tt.name == "stream bootstrap" {
				m.RegisterExecutor(&policyBootstrapRefreshExecutor{unauthorizedRefreshExecutor: executor})
			}
			if tt.name == "count tokens" {
				countExecutor := &policyCountRefreshExecutor{unauthorizedRefreshExecutor: executor}
				m.RegisterExecutor(countExecutor)
				if err := tt.run(m, model, opts); err == nil {
					t.Fatal("CountTokens() error = nil, want policy denial")
				}
				if !reflect.DeepEqual(countExecutor.calls, []string{primary.ID}) {
					t.Fatalf("CountTokens calls = %v, want only initial %q call", countExecutor.calls, primary.ID)
				}
			} else {
				var terminated *cliproxyexecutor.RequestTerminatedError
				if err := tt.run(m, model, opts); !errors.As(err, &terminated) || terminated.HTTPStatus != http.StatusForbidden {
					t.Fatalf("%s error = %v, want policy denial 403", tt.name, err)
				}
				calls := tt.calls(executor)
				if !reflect.DeepEqual(calls, []string{primary.ID}) {
					t.Fatalf("%s calls = %v, want only initial %q call", tt.name, calls, primary.ID)
				}
			}
			if len(attemptIDs) != 2 || attemptIDs[0] == "" || attemptIDs[1] == "" || attemptIDs[0] == attemptIDs[1] {
				t.Fatalf("policy attempt IDs = %v, want two distinct non-empty IDs", attemptIDs)
			}
		})
	}
}

func TestManager_StreamRequiredRequestPolicyStopsBeforeUpstream(t *testing.T) {
	for _, callbackPresent := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing callback", true: "denied"}[callbackPresent], func(t *testing.T) {
			m, executor, _, _, model := newUnauthorizedRefreshFixture(t, false)
			opts := cliproxyexecutor.Options{RequestAfterAuthInterceptorRequired: true}
			wantStatus := http.StatusServiceUnavailable
			if callbackPresent {
				wantStatus = http.StatusForbidden
				opts.RequestAfterAuthInterceptor = func(context.Context, cliproxyexecutor.RequestAfterAuthInterceptRequest) cliproxyexecutor.RequestAfterAuthInterceptResponse {
					return cliproxyexecutor.RequestAfterAuthInterceptResponse{Terminate: true, StatusCode: http.StatusForbidden}
				}
			}
			_, err := m.ExecuteStream(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: model}, opts)
			var terminated *cliproxyexecutor.RequestTerminatedError
			if !errors.As(err, &terminated) || terminated.HTTPStatus != wantStatus {
				t.Fatalf("ExecuteStream error = %v, want termination %d", err, wantStatus)
			}
			if calls := executor.StreamCalls(); len(calls) != 0 {
				t.Fatalf("policy termination dispatched upstream: %v", calls)
			}
		})
	}
}

func TestApplyRequestAfterAuthInterceptorRequiredWithoutCallbackFailsClosed(t *testing.T) {
	_, _, err := applyRequestAfterAuthInterceptor(context.Background(), nil, "codex", cliproxyexecutor.Request{}, cliproxyexecutor.Options{RequestAfterAuthInterceptorRequired: true}, "model")
	var terminated *cliproxyexecutor.RequestTerminatedError
	if !errors.As(err, &terminated) || terminated.HTTPStatus != http.StatusServiceUnavailable {
		t.Fatalf("error = %v, want fail-closed 503 RequestTerminatedError", err)
	}
}

func TestProviderPoolQuotaIncludesUnavailableAccountsAndIsolatesSignals(t *testing.T) {
	m := NewManager(nil, nil, nil)
	observed := time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC)
	for _, auth := range []*Auth{
		{ID: "cooldown", Provider: "codex", Unavailable: true, Quota: QuotaState{ObservedAt: observed, Signals: map[string]string{"X-Codex-Primary-Used-Percent": "80"}}},
		{ID: "disabled", Provider: "codex", Disabled: true, Quota: QuotaState{ObservedAt: observed}},
		{ID: "other", Provider: "claude", Quota: QuotaState{ObservedAt: observed}},
	} {
		if _, err := m.Register(context.Background(), auth); err != nil {
			t.Fatal(err)
		}
	}
	got := m.providerPoolQuota("codex")
	if len(got) != 1 || got[0].AuthID != "cooldown" || !got[0].ObservedAt.Equal(observed) || got[0].Signals["X-Codex-Primary-Used-Percent"] != "80" {
		t.Fatalf("pool quota=%#v", got)
	}
	got[0].Signals["X-Codex-Primary-Used-Percent"] = "mutated"
	if again := m.providerPoolQuota("codex"); again[0].Signals["X-Codex-Primary-Used-Percent"] != "80" {
		t.Fatal("quota snapshot mutated authoritative auth state")
	}
	opts := cliproxyexecutor.Options{ProviderPoolQuota: m.providerPoolQuota("codex"), RequestAfterAuthInterceptor: func(_ context.Context, request cliproxyexecutor.RequestAfterAuthInterceptRequest) cliproxyexecutor.RequestAfterAuthInterceptResponse {
		if len(request.ProviderPoolQuota) != 1 || !request.ProviderPoolQuota[0].ObservedAt.Equal(observed) {
			t.Fatalf("after-auth pool quota=%#v", request.ProviderPoolQuota)
		}
		request.ProviderPoolQuota[0].Signals["X-Codex-Primary-Used-Percent"] = "interceptor"
		return cliproxyexecutor.RequestAfterAuthInterceptResponse{}
	}}
	if _, _, err := applyRequestAfterAuthInterceptor(context.Background(), nil, "codex", cliproxyexecutor.Request{}, opts, "model"); err != nil {
		t.Fatal(err)
	}
	if opts.ProviderPoolQuota[0].Signals["X-Codex-Primary-Used-Percent"] != "80" {
		t.Fatal("interceptor changed authoritative quota options")
	}
}

func TestPluginSchedulerReceivesTotalEnabledPoolsBeyondAvailableCandidates(t *testing.T) {
	m := NewManager(nil, nil, nil)
	observed := time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC)
	ready := &Auth{ID: "ready", Provider: "codex"}
	for _, auth := range []*Auth{ready, {ID: "cooldown", Provider: "codex", Unavailable: true, Quota: QuotaState{ObservedAt: observed, Signals: map[string]string{"X-Codex-Primary-Used-Percent": "90"}}}, {ID: "disabled", Provider: "codex", Disabled: true}, {ID: "claude", Provider: "claude"}} {
		if _, err := m.Register(context.Background(), auth); err != nil {
			t.Fatal(err)
		}
	}
	scheduler := &fakePluginScheduler{}
	if _, _, err := m.pickViaPluginScheduler(context.Background(), scheduler, "mixed", []string{"codex", "claude"}, "model", cliproxyexecutor.Options{}, nil, []*Auth{ready}); err != nil {
		t.Fatal(err)
	}
	if len(scheduler.requests) != 1 {
		t.Fatalf("scheduler calls=%d", len(scheduler.requests))
	}
	request := scheduler.requests[0]
	if len(request.Candidates) != 1 || len(request.ProviderPools["codex"]) != 2 || len(request.ProviderPools["claude"]) != 1 {
		t.Fatalf("candidate/pool inventory mismatch: %#v", request)
	}
	if codex := request.ProviderPools["codex"]; codex[0].AuthID != "cooldown" || !codex[0].ObservedAt.Equal(observed) || codex[0].Signals["X-Codex-Primary-Used-Percent"] != "90" {
		t.Fatalf("quota inventory lost: %#v", codex)
	}
}
