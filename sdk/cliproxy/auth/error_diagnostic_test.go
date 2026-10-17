package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/redisqueue"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

const privateDiagnosticPoison = "SYNTHETIC_PRIVATE_DIAGNOSTIC_POISON"
const privateDiagnosticSafe = "upstream request failed (HTTP 429)"

type privateDiagnosticLifecycle struct {
	PrivateRuntimeState string
}

func (*privateDiagnosticLifecycle) Bind(func() error) error { return nil }
func (*privateDiagnosticLifecycle) End(string)              {}

func TestPrivateErrorDiagnosticResultJSONExcludesRuntimeOptions(t *testing.T) {
	// Without the diagnostic serializer, even a zero Result fails on the typed
	// function fields in Options. There is no successful legacy JSON payload.
	type legacyResult Result
	_, err := json.Marshal(legacyResult{})
	var unsupported *json.UnsupportedTypeError
	if !errors.As(err, &unsupported) || unsupported.Type.Kind() != reflect.Func {
		t.Fatalf("legacy result serialization error = %v, want unsupported func", err)
	}

	retryAfter := 19 * time.Second
	result := Result{
		AuthID: "diagnostic-options", Provider: "codex", Model: "private-model", RouteModel: "route-model",
		RetryAfter: &retryAfter, CredentialScope: true, SkipQuotaObservation: true,
		CredentialVersion: 7, RegistrationEpoch: 9,
		Options: cliproxyexecutor.Options{
			Stream: true, Alt: "json", Headers: http.Header{"X-Test": {"safe-value"}},
			Query: map[string][]string{"test": {"safe-value"}}, OriginalRequest: []byte(`{"model":"test"}`),
			SourceFormat: "openai", ResponseFormat: "claude", Metadata: map[string]any{"hint": "safe-value"},
			RequestAfterAuthInterceptorRequired: true, ProviderPoolAuthIDs: []string{"auth-id"},
			ProxyURL: "http://proxy.invalid",
			RequestAfterAuthInterceptor: func(context.Context, cliproxyexecutor.RequestAfterAuthInterceptRequest) cliproxyexecutor.RequestAfterAuthInterceptResponse {
				t.Fatal("serialization invoked the interceptor")
				return cliproxyexecutor.RequestAfterAuthInterceptResponse{}
			},
			WebSocketResponseObserver: func(context.Context, cliproxyexecutor.WebSocketResponseEvent) {
				t.Fatal("serialization invoked the observer")
			},
			ExecutionLifecycle: &privateDiagnosticLifecycle{PrivateRuntimeState: privateDiagnosticPoison},
		},
	}.withError(markedDiagnosticStatusError{message: privateDiagnosticPoison, diagnostic: privateDiagnosticSafe, statusCode: 429})
	for _, value := range []any{result, &result} {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		assertPrivateDiagnosticText(t, string(data), privateDiagnosticSafe)
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(data, &fields); err != nil {
			t.Fatal(err)
		}
		var options map[string]json.RawMessage
		if err := json.Unmarshal(fields["Options"], &options); err != nil {
			t.Fatal(err)
		}
		optionValue := reflect.ValueOf(result.Options)
		for i := 0; i < optionValue.NumField(); i++ {
			name := optionValue.Type().Field(i).Name
			switch name {
			case "RequestAfterAuthInterceptor", "WebSocketResponseObserver", "ExecutionLifecycle":
				if _, exists := options[name]; exists {
					t.Fatalf("runtime-only option %s was serialized", name)
				}
			default:
				want, err := json.Marshal(optionValue.Field(i).Interface())
				if err != nil {
					t.Fatal(err)
				}
				if string(options[name]) != string(want) {
					t.Fatalf("option %s changed: got %s, want %s", name, options[name], want)
				}
			}
		}
		var decoded Result
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatal(err)
		}
		if decoded.Error.Message != privateDiagnosticSafe || decoded.Error.HTTPStatus != 429 ||
			decoded.AuthID != result.AuthID || decoded.Provider != result.Provider || decoded.Model != result.Model ||
			decoded.RouteModel != result.RouteModel || decoded.Success != result.Success ||
			decoded.RetryAfter == nil || *decoded.RetryAfter != retryAfter || !decoded.CredentialScope ||
			!decoded.SkipQuotaObservation || decoded.CredentialVersion != 7 || decoded.RegistrationEpoch != 9 {
			t.Fatalf("result fields changed during serialization: %+v", decoded)
		}
	}
	if result.Error.Message != privateDiagnosticPoison || result.Options.RequestAfterAuthInterceptor == nil ||
		result.Options.WebSocketResponseObserver == nil || result.Options.ExecutionLifecycle == nil {
		t.Fatal("serialization mutated live execution state")
	}
}

func assertPrivateDiagnosticJSON(t *testing.T, value any, want string) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	assertPrivateDiagnosticText(t, string(data), want)
}

func assertPrivateDiagnosticText(t *testing.T, text, want string) {
	t.Helper()
	if strings.Contains(text, privateDiagnosticPoison) || strings.Contains(text, "SYNTHETIC_SIGNED_QUERY") {
		t.Fatalf("private diagnostic reached observability sink: %s", text)
	}
	if !strings.Contains(text, want) {
		t.Fatalf("diagnostic %q missing from sink: %s", want, text)
	}
}

func TestPrivateErrorDiagnosticManagerEventsAndSnapshots(t *testing.T) {
	previousCooldown := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previousCooldown) })
	for _, model := range []string{"", "private-test-model"} {
		for _, private := range []bool{false, true} {
			t.Run(fmt.Sprintf("model=%s/private=%t", model, private), func(t *testing.T) {
				withEnabledErrorQueue(t)
				subscriber, unsubscribe := redisqueue.SubscribeErrors()
				defer unsubscribe()
				message := "ordinary upstream quota"
				diagnostic := message
				if private {
					message = privateDiagnosticPoison + " https://invalid.example/blob?sig=SYNTHETIC_SIGNED_QUERY"
					diagnostic = privateDiagnosticSafe
				}
				upstream := fmt.Errorf("outer: %w", markedDiagnosticStatusError{message: message, diagnostic: diagnostic, statusCode: http.StatusTooManyRequests})
				wantDiagnostic := upstream.Error()
				if private {
					wantDiagnostic = diagnostic
				}
				manager := NewManager(nil, nil, nil)
				auth := &Auth{ID: "private-diagnostic-auth", Provider: "codex", Status: StatusActive}
				if _, err := manager.Register(WithSkipPersist(context.Background()), auth); err != nil {
					t.Fatal(err)
				}
				retryAfter := 37 * time.Second
				result := Result{AuthID: auth.ID, Provider: auth.Provider, Model: model, RetryAfter: &retryAfter, CredentialScope: true}.withError(upstream)
				manager.MarkResult(context.Background(), result)
				payload := requireErrorSubscriberPayload(t, subscriber)
				var event errorEvent
				if err := json.Unmarshal(payload, &event); err != nil {
					t.Fatal(err)
				}
				if event.Body != wantDiagnostic || event.StatusCode != http.StatusTooManyRequests {
					t.Fatalf("event lost error semantics: %+v", event)
				}
				assertPrivateDiagnosticText(t, string(payload), wantDiagnostic)
				assertPrivateDiagnosticJSON(t, result, wantDiagnostic)
				assertPrivateDiagnosticText(t, result.LogDiagnostic(), wantDiagnostic)
				if result.Error.Message != upstream.Error() || upstream.Error() != "outer: "+message {
					t.Fatal("conversion changed caller/classifier message")
				}
				current, ok := manager.GetByID(auth.ID)
				if !ok || current.LastError == nil || current.LastError.Message != upstream.Error() {
					t.Fatalf("runtime classifier input lost: %+v", current)
				}
				if !current.Quota.Exceeded || current.NextRetryAfter.Sub(current.UpdatedAt) != retryAfter {
					t.Fatalf("credential quota/retry changed: %+v", current)
				}
				assertPrivateDiagnosticJSON(t, current, wantDiagnostic)
				assertPrivateDiagnosticJSON(t, *current.Clone(), wantDiagnostic)
				record, ok := authCooldownStateRecord(current, current.UpdatedAt)
				if !ok || record.LastError.Message != wantDiagnostic {
					t.Fatalf("credential snapshot diagnostic = %+v", record)
				}
				records := []CooldownStateRecord{record}
				if model != "" {
					state := current.ModelStates[model]
					if state == nil || state.LastError.Message != upstream.Error() || state.StatusMessage != wantDiagnostic {
						t.Fatalf("model state lost classification or diagnostic: %+v", state)
					}
					if !state.Quota.Exceeded || state.NextRetryAfter.Sub(state.UpdatedAt) != retryAfter {
						t.Fatalf("model quota/retry changed: %+v", state)
					}
					assertPrivateDiagnosticJSON(t, state.Clone(), wantDiagnostic)
					assertPrivateDiagnosticJSON(t, *state.Clone(), wantDiagnostic)
					for _, newer := range []bool{false, true} {
						other := &ModelState{}
						if newer {
							other.UpdatedAt = state.UpdatedAt.Add(time.Second)
						}
						assertPrivateDiagnosticJSON(t, mergeModelState(other, state.Clone()), wantDiagnostic)
					}
					modelRecord, ok := modelCooldownStateRecord(current, model, state, state.UpdatedAt)
					if !ok || modelRecord.LastError.Message != wantDiagnostic {
						t.Fatalf("model snapshot diagnostic = %+v", modelRecord)
					}
					records = append(records, modelRecord)
				}
				assertPrivateDiagnosticJSON(t, records, wantDiagnostic)
				store := NewFileCooldownStateStore(t.TempDir())
				if err := store.Save(context.Background(), records); err != nil {
					t.Fatal(err)
				}
				loaded, err := store.Load(context.Background())
				if err != nil || len(loaded) != len(records) {
					t.Fatalf("load persisted diagnostics: count=%d err=%v", len(loaded), err)
				}
				assertPrivateDiagnosticJSON(t, loaded, wantDiagnostic)
			})
		}
	}
}

type privateDiagnosticRefreshEvaluator struct{}

func (privateDiagnosticRefreshEvaluator) ShouldRefresh(time.Time, *Auth) bool { return true }

func TestPrivateErrorDiagnosticRestorePreservesClassifiers(t *testing.T) {
	previousCooldown := quotaCooldownDisabled.Load()
	quotaCooldownDisabled.Store(false)
	t.Cleanup(func() { quotaCooldownDisabled.Store(previousCooldown) })
	for _, tc := range []struct {
		name, message, reason string
		status                int
	}{
		{"invalid-grant", "invalid_grant", "invalid_grant", 400},
		{"model-support", "requested model is not supported", "model_not_supported", 400},
		{"model-support-422", "not available for your plan", "model_not_supported", 422},
		{"cloudflare", "cf-mitigated", "cloudflare_challenge", 403},
	} {
		for _, model := range []string{"", "private-restore-model"} {
			t.Run(tc.name+"/model="+model, func(t *testing.T) {
				ctx := context.Background()
				manager := NewManager(nil, nil, nil)
				auth := &Auth{ID: "private-restore-auth", Provider: "codex", Status: StatusActive}
				if _, err := manager.Register(WithSkipPersist(ctx), auth); err != nil {
					t.Fatal(err)
				}
				upstream := markedDiagnosticStatusError{
					message:    tc.message + " " + privateDiagnosticPoison + " https://invalid.example/?sig=SYNTHETIC_SIGNED_QUERY",
					diagnostic: fmt.Sprintf("upstream request failed (HTTP %d)", tc.status), statusCode: tc.status,
				}
				manager.MarkResult(ctx, Result{AuthID: auth.ID, Provider: auth.Provider, Model: model}.withError(upstream))
				before, ok := manager.GetByID(auth.ID)
				if !ok || before.LastError == nil || before.LastError.Message != upstream.message {
					t.Fatalf("live classifier input changed: %+v", before)
				}
				projectionNow := before.UpdatedAt
				wantViews := CooldownSnapshotForAuth(before, projectionNow)
				if len(wantViews) != 1 || wantViews[0].Reason != tc.reason {
					t.Fatalf("unexpected initial cooldown classification: %+v", wantViews)
				}
				store := NewFileCooldownStateStore(t.TempDir())
				if err := store.Save(ctx, manager.cooldownStateRecordsSnapshot()); err != nil {
					t.Fatal(err)
				}
				loaded, err := store.Load(ctx)
				if err != nil || len(loaded) == 0 {
					t.Fatalf("load cooldown state: records=%d err=%v", len(loaded), err)
				}
				assertPrivateDiagnosticJSON(t, loaded, upstream.diagnostic)
				restoredManager := NewManager(nil, nil, nil)
				if _, err := restoredManager.Register(WithSkipPersist(ctx), &Auth{ID: auth.ID, Provider: auth.Provider, Status: StatusActive}); err != nil {
					t.Fatal(err)
				}
				restoredManager.SetCooldownStateStore(store)
				if err := restoredManager.RestoreCooldownStates(ctx); err != nil {
					t.Fatal(err)
				}
				after, ok := restoredManager.GetByID(auth.ID)
				if !ok {
					t.Fatal("restored auth missing")
				}
				if got := CooldownSnapshotForAuth(after, projectionNow); !reflect.DeepEqual(got, wantViews) {
					t.Fatalf("restored cooldown classification changed: got %+v, want %+v", got, wantViews)
				}
				assertPrivateDiagnosticJSON(t, after, upstream.diagnostic)
				beforeError, afterError := before.LastError, after.LastError
				if model != "" {
					beforeError, afterError = before.ModelStates[model].LastError, after.ModelStates[model].LastError
				}
				if afterError == nil || afterError.HTTPStatus != beforeError.HTTPStatus ||
					isModelSupportResultError(afterError) != isModelSupportResultError(beforeError) ||
					isInvalidGrantResultError(afterError) != isInvalidGrantResultError(beforeError) ||
					isCloudflareChallengeResultError(afterError) != isCloudflareChallengeResultError(beforeError) {
					t.Fatalf("restored classifiers changed: before=%+v after=%+v", beforeError, afterError)
				}
				before.Runtime, after.Runtime = privateDiagnosticRefreshEvaluator{}, privateDiagnosticRefreshEvaluator{}
				if got, want := restoredManager.shouldRefresh(after, projectionNow), manager.shouldRefresh(before, projectionNow); got != want {
					t.Fatalf("restore changed refresh eligibility: got %t want %t", got, want)
				}
				if tc.reason == "invalid_grant" && model == "" {
					after.Disabled = true
					if !HasDisabledInvalidGrantFailure(after) || restoredManager.shouldRefresh(after, projectionNow) {
						t.Fatal("restored invalid_grant no longer blocks disabled-auth refresh")
					}
				}
				// Restore itself saves again: the canonical tags must survive that pass too.
				resaved, err := store.Load(ctx)
				if err != nil {
					t.Fatal(err)
				}
				assertPrivateDiagnosticJSON(t, resaved, upstream.diagnostic)
			})
		}
	}
}

func TestPrivateErrorDiagnosticJSONPreservesDisabledRefreshGate(t *testing.T) {
	manager := NewManager(nil, nil, nil)
	now := time.Now()
	for _, status := range []int{400, 503} {
		result := Result{}.withError(markedDiagnosticStatusError{
			message: "invalid_grant " + privateDiagnosticPoison, diagnostic: "upstream failure", statusCode: status,
		})
		original := &Auth{Disabled: true, LastError: result.Error, errorDiagnostic: result.errorDiagnostic, Runtime: privateDiagnosticRefreshEvaluator{}}
		data, err := json.Marshal(original)
		if err != nil {
			t.Fatal(err)
		}
		assertPrivateDiagnosticText(t, string(data), "[invalid_grant]")
		var restored Auth
		if err := json.Unmarshal(data, &restored); err != nil {
			t.Fatal(err)
		}
		restored.Runtime = privateDiagnosticRefreshEvaluator{}
		if !HasDisabledInvalidGrantFailure(&restored) || manager.shouldRefresh(&restored, now) || manager.shouldRefresh(original, now) {
			t.Fatalf("JSON round trip changed disabled invalid_grant refresh gate for HTTP %d", status)
		}
		restored.LastError = &Error{Message: "ordinary replacement", HTTPStatus: status}
		if !manager.shouldRefresh(&restored, now) {
			t.Fatal("refresh control did not reach the evaluator after replacing invalid_grant")
		}
	}
}

func TestPrivateErrorDiagnosticPreservesClassifiersAndReplacement(t *testing.T) {
	for _, message := range []string{"model not supported", "invalid_grant", "connection reset by peer", "file not found"} {
		t.Run(message, func(t *testing.T) {
			upstream := markedDiagnosticStatusError{message: message + " " + privateDiagnosticPoison, diagnostic: "upstream request failed (HTTP 400)", statusCode: http.StatusBadRequest}
			result := Result{}.withError(upstream)
			if !reflect.DeepEqual(result.Error, resultErrorFromError(upstream)) || result.Error.Message != upstream.Error() {
				t.Fatalf("private conversion changed classifier input: %+v", result.Error)
			}
			if message == "invalid_grant" {
				auth := &Auth{Disabled: true, LastError: result.Error, errorDiagnostic: result.errorDiagnostic}
				if !hasDisabledInvalidGrantFailure(auth) {
					t.Fatal("private state lost body-based invalid_grant classification")
				}
				assertPrivateDiagnosticJSON(t, auth, upstream.diagnostic)
			}
			result.Error = &Error{Code: "ordinary", Message: "ordinary replacement", HTTPStatus: 503}
			assertPrivateDiagnosticJSON(t, result, "ordinary replacement")
		})
	}
}

func TestPrivateErrorDiagnosticManagerExecutePreservesCallerAndWarnLog(t *testing.T) {
	withEnabledErrorQueue(t)
	subscriber, unsubscribe := redisqueue.SubscribeErrors()
	defer unsubscribe()
	hook := setupTestLoggerHook(t)
	manager := NewManager(nil, nil, nil)
	auth := &Auth{ID: "private-diagnostic-execute", Provider: "codex", Status: StatusActive}
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "codex", []*registry.ModelInfo{{ID: "private-test-model"}})
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
	if _, err := manager.Register(WithSkipPersist(context.Background()), auth); err != nil {
		t.Fatal(err)
	}
	upstream := markedDiagnosticStatusError{message: privateDiagnosticPoison, diagnostic: privateDiagnosticSafe, statusCode: http.StatusTooManyRequests}
	manager.RegisterExecutor(&mockCustomErrorExecutor{
		identifier: "codex",
		executeFn: func(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
			return cliproxyexecutor.Response{}, upstream
		},
	})
	_, err := manager.Execute(context.Background(), []string{"codex"}, cliproxyexecutor.Request{Model: "private-test-model"}, cliproxyexecutor.Options{})
	if err == nil || err.Error() != privateDiagnosticPoison || statusCodeFromError(err) != http.StatusTooManyRequests {
		t.Fatalf("manager flattened caller error: %v", err)
	}
	assertPrivateDiagnosticText(t, string(requireErrorSubscriberPayload(t, subscriber)), privateDiagnosticSafe)
	assertPrivateDiagnosticText(t, errorString(err), privateDiagnosticSafe)
	found := false
	for _, entry := range hook.AllEntries() {
		if strings.Contains(entry.Message, "upstream execution failed") {
			found = true
			assertPrivateDiagnosticText(t, entry.Message, privateDiagnosticSafe)
		}
	}
	if !found {
		t.Fatal("manager did not emit upstream failure WARN")
	}
}
