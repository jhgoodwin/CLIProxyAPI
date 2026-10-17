package helps

import (
	"context"

	"github.com/gin-gonic/gin"
)

type sharedFilePrivacyKey struct{}

// FilePrivacyLogSuppressorKey holds the request logging owner's suppression interface in Gin.
const FilePrivacyLogSuppressorKey = "FILE_PRIVACY_LOG_SUPPRESSOR"

// WithFilePrivacy suppresses upstream and downstream capture for local file requests.
// Call it before resolving image content or writing response headers. The logging owner
// removes any temporary captures allocated before local references were recognized.
func WithFilePrivacy(ctx context.Context) context.Context {
	if c := ginContextFrom(ctx); c != nil {
		if c.Request != nil {
			c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), sharedFilePrivacyKey{}, true))
		}
		if owner, exists := c.Get(FilePrivacyLogSuppressorKey); exists {
			if suppressor, ok := owner.(interface{ SuppressRequestLogging(*gin.Context) }); ok {
				suppressor.SuppressRequestLogging(c)
			}
		}
	}
	return context.WithValue(ctx, sharedFilePrivacyKey{}, true)
}
func sharedFilePrivacyContext(ctx context.Context) bool {
	private, _ := ctx.Value(sharedFilePrivacyKey{}).(bool)
	return private
}
