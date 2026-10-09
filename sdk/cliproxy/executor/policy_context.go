package executor

import "context"

type attemptIDContextKey struct{}

// WithAttemptID binds an authorization attempt to subsequent usage events.
func WithAttemptID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, attemptIDContextKey{}, id)
}

// AttemptIDFromContext returns the host-issued authorization attempt identifier.
func AttemptIDFromContext(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.Value(attemptIDContextKey{}).(string)
	return id
}
