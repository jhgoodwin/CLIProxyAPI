package executor

import (
	"fmt"
	"net/http"
	"net/http/httptest"

	auth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// filesFixtureTransport retains official HTTPS request URLs without opening sockets.
// Every unexpected origin fails closed rather than reaching a real service.
type filesFixtureTransport map[string]http.Handler

func (f filesFixtureTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	h := f[r.URL.Scheme+"://"+r.URL.Host]
	if h == nil {
		return nil, fmt.Errorf("unexpected Files fixture origin: %s", r.URL.Host)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	response := w.Result()
	response.Request = r
	return response, nil
}

func (f filesFixtureTransport) RoundTripperFor(_ *auth.Auth) http.RoundTripper { return f }
