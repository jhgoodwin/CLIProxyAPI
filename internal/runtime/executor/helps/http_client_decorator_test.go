package helps

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type decoratorTestTransport struct{ base http.RoundTripper }

func (rt *decoratorTestTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return rt.base.RoundTrip(req)
}

func TestOperationHTTPClientKeepsUnmodifiedClientWithoutDecorator(t *testing.T) {
	original := &http.Client{Timeout: time.Second}
	for _, ctx := range []context.Context{nil, context.Background(), WithHTTPClientDecorator(context.Background(), nil)} {
		if got := operationHTTPClient(ctx, original); got != original {
			t.Fatal("execution without decorator changed client")
		}
	}
}

func TestOperationHTTPClientCopiesClientBeforeDecoration(t *testing.T) {
	transport := &http.Transport{}
	original := &http.Client{Transport: transport, Timeout: time.Second}
	ctx := WithHTTPClientDecorator(context.Background(), func(local *http.Client) *http.Client {
		if local == original || local.Transport != transport || local.Timeout != time.Second {
			t.Fatal("decorator did not receive a copy retaining selected settings")
		}
		local.Transport = &decoratorTestTransport{base: local.Transport}
		local.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		return local
	})
	got := operationHTTPClient(ctx, original)
	if got == original || got.CheckRedirect == nil || original.CheckRedirect != nil || original.Transport != transport {
		t.Fatal("decoration changed original client or lost redirect policy")
	}
}

func TestHTTPClientDecoratorWrapsResolvedTransport(t *testing.T) {
	for _, fingerprint := range []bool{false, true} {
		for _, proxy := range []bool{false, true} {
			name := "standard"
			if fingerprint {
				name = "fingerprinted"
			}
			if proxy {
				name += "/proxy"
			} else {
				name += "/context"
			}
			t.Run(name, func(t *testing.T) {
				injected := &http.Transport{}
				cfg := &config.Config{}
				selected := &cliproxyauth.Auth{}
				if proxy {
					selected.ProxyURL = "http://127.0.0.1:9"
				}
				ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(injected))
				calls := 0
				var resolved http.RoundTripper
				ctx = WithHTTPClientDecorator(ctx, func(local *http.Client) *http.Client {
					calls++
					resolved = local.Transport
					if local.Timeout != 3*time.Second {
						t.Fatal("timeout lost before decoration")
					}
					local.Transport = &decoratorTestTransport{base: local.Transport}
					local.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
					return local
				})
				var got *http.Client
				if fingerprint {
					got = NewUtlsHTTPClient(ctx, cfg, selected, 3*time.Second)
				} else {
					got = NewProxyAwareHTTPClient(ctx, cfg, selected, 3*time.Second)
				}
				wrapped, ok := got.Transport.(*decoratorTestTransport)
				if calls != 1 || !ok || wrapped.base != resolved || got.CheckRedirect == nil {
					t.Fatal("resolved client not decorated exactly once")
				}
				if fingerprint {
					split, ok := resolved.(*fallbackRoundTripper)
					if !ok {
						t.Fatal("provider TLS routing transport was replaced")
					}
					if proxy {
						if split.chrome == injected || split.anthropic == injected || split.fallback == injected {
							t.Fatal("context transport bypassed proxy selection")
						}
					} else if split.chrome != injected || split.anthropic != injected || split.fallback != injected {
						t.Fatal("context transport selection changed")
					}
				} else if proxy {
					if resolved == injected {
						t.Fatal("context transport bypassed selected proxy")
					}
					selectedTransport, ok := resolved.(*http.Transport)
					if !ok || selectedTransport.Proxy == nil {
						t.Fatal("selected proxy transport lost")
					}
					endpoint, err := selectedTransport.Proxy(&http.Request{})
					if err != nil || endpoint == nil || endpoint.String() != selected.ProxyURL {
						t.Fatal("selected proxy endpoint changed")
					}
				} else if resolved != injected {
					t.Fatal("context transport selection changed")
				}
			})
		}
	}
}
