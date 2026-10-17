package files

import "github.com/gin-gonic/gin"

// AuthenticationError writes authentication failures using the caller's Files format.
func AuthenticationError(c *gin.Context, status int, message string) {
	kind := "authentication_error"
	if status >= 500 {
		kind = "api_error"
	}
	failure(c, status, kind, message)
}
