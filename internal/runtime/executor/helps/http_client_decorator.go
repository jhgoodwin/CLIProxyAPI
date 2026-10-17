package helps

import (
	"context"
	"net/http"
)

// HTTPClientDecorator adapts an execution-local client after proxy and TLS selection.
// It must return a non-nil client and must not mutate the selected transport.
type HTTPClientDecorator func(*http.Client) *http.Client

type httpClientDecoratorKey struct{}

// WithHTTPClientDecorator installs a decorator for clients created in this execution.
// The decorator receives a shallow client copy; transports remain shared.
func WithHTTPClientDecorator(ctx context.Context, decorate HTTPClientDecorator) context.Context {
	return context.WithValue(ctx, httpClientDecoratorKey{}, decorate)
}

func operationHTTPClient(ctx context.Context, client *http.Client) *http.Client {
	if ctx == nil {
		return client
	}
	decorate, _ := ctx.Value(httpClientDecoratorKey{}).(HTTPClientDecorator)
	if decorate == nil {
		return client
	}
	local := *client
	return decorate(&local)
}
