package helps

import "regexp"

// Anthropic local file IDs are namespaced by the authenticated principal.
// Recognize only the proxy format; upstream file IDs must remain usable.
var anthropicLocalFileID = regexp.MustCompile(`^file_[0-9a-f]{64}_[0-9a-f]{16}_[0-9a-f]{32}$`)

func IsProxyLocalFileID(id string) bool {
	return anthropicLocalFileID.MatchString(id)
}
