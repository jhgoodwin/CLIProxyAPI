package auth

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// errorDiagnostic keeps private observability text separate from classifier input.
// Binding it to the error prevents a later replacement from inheriting stale text.
// Error itself retains its public four-field layout for SDK unkeyed literals.
type errorDiagnostic struct {
	source  *Error
	message string
}

func (d errorDiagnostic) cloneFor(source, cloned *Error) errorDiagnostic {
	if source == nil || d.source != source || d.message == "" {
		return errorDiagnostic{}
	}
	return errorDiagnostic{source: cloned, message: d.message}
}

func (d errorDiagnostic) diagnosticError(err *Error) *Error {
	if err == nil || d.source != err || d.message == "" {
		return err
	}
	copyError := *err
	copyError.Message = d.message
	// Closed classifier tags preserve restored state without retaining body text.
	if isInvalidGrantErrorMessage(err.Message) && !isInvalidGrantErrorMessage(copyError.Message) {
		copyError.Message += " [invalid_grant]"
	}
	if isModelSupportResultError(err) && !isModelSupportResultError(&copyError) {
		copyError.Message += " [model_not_supported]"
	}
	if isCloudflareChallengeResultError(err) && !isCloudflareChallengeResultError(&copyError) {
		copyError.Message += " [cloudflare challenge]"
	}
	return &copyError
}

// withError preserves the full message for routing/cooldown rules and captures
// the executor's private diagnostic for logs, events, and persisted snapshots.
func (r Result) withError(err error) Result {
	r.Error = resultErrorFromError(err)
	r.errorDiagnostic = errorDiagnostic{}
	var diagnostic interface {
		error
		LogDiagnostic() string
	}
	if errors.As(err, &diagnostic) && diagnostic != nil {
		message := diagnostic.LogDiagnostic()
		if strings.TrimSpace(message) != "" && message != diagnostic.Error() {
			r.errorDiagnostic = errorDiagnostic{source: r.Error, message: safeErrorDiagnosticForLog(err)}
		}
	}
	return r
}

func (r Result) diagnosticError() *Error {
	return r.errorDiagnostic.diagnosticError(r.Error)
}

// LogDiagnostic returns observability text without changing classification input.
func (r Result) LogDiagnostic() string {
	return r.diagnosticError().Error()
}

// resultOptionsJSON excludes callbacks and lifecycle ownership from telemetry.
// The remaining fields keep Options' default JSON names and value encoding.
type resultOptionsJSON struct {
	Stream                              bool
	Alt                                 string
	Headers                             http.Header
	Query                               url.Values
	OriginalRequest                     []byte
	SourceFormat                        sdktranslator.Format
	ResponseFormat                      sdktranslator.Format
	Metadata                            map[string]any
	RequestAfterAuthInterceptorRequired bool
	ProviderPoolQuota                   []pluginapi.ProviderQuotaObservation
	ProviderPoolAuthIDs                 []string
	ProxyURL                            string
}

// MarshalJSON excludes private upstream text and runtime-only execution resources.
func (r Result) MarshalJSON() ([]byte, error) {
	type resultJSON Result
	copyResult := resultJSON(r)
	copyResult.Error = r.diagnosticError()
	return json.Marshal(struct {
		resultJSON
		Options resultOptionsJSON
	}{
		resultJSON: copyResult,
		Options: resultOptionsJSON{
			Stream:                              r.Options.Stream,
			Alt:                                 r.Options.Alt,
			Headers:                             r.Options.Headers,
			Query:                               r.Options.Query,
			OriginalRequest:                     r.Options.OriginalRequest,
			SourceFormat:                        r.Options.SourceFormat,
			ResponseFormat:                      r.Options.ResponseFormat,
			Metadata:                            r.Options.Metadata,
			RequestAfterAuthInterceptorRequired: r.Options.RequestAfterAuthInterceptorRequired,
			ProviderPoolQuota:                   r.Options.ProviderPoolQuota,
			ProviderPoolAuthIDs:                 r.Options.ProviderPoolAuthIDs,
			ProxyURL:                            r.Options.ProxyURL,
		},
	})
}

// MarshalJSON keeps raw LastError available to runtime classifiers, not storage.
func (a Auth) MarshalJSON() ([]byte, error) {
	type authJSON Auth
	copyAuth := authJSON(a)
	copyAuth.LastError = a.errorDiagnostic.diagnosticError(a.LastError)
	return json.Marshal(copyAuth)
}

// MarshalJSON applies the same private diagnostic policy to standalone models.
func (m ModelState) MarshalJSON() ([]byte, error) {
	type modelStateJSON ModelState
	copyState := modelStateJSON(m)
	copyState.LastError = m.errorDiagnostic.diagnosticError(m.LastError)
	return json.Marshal(copyState)
}
