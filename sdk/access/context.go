package access

import "context"

type principalContextKey struct{}

// WithAuthenticatedPrincipal records the subject validated by frontend authentication.
func WithAuthenticatedPrincipal(ctx context.Context, principal string) context.Context {
	return context.WithValue(ctx, principalContextKey{}, principal)
}

// AuthenticatedPrincipal returns the subject recorded by frontend authentication.
func AuthenticatedPrincipal(ctx context.Context) string {
	if ctx == nil {
		return ""
	}
	principal, _ := ctx.Value(principalContextKey{}).(string)
	return principal
}
