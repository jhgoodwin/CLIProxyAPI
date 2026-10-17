package helps

import (
	"context"
	"fmt"
)

// ValidateGeminiPendingPreservation rejects local image parts dropped by translation.
// Nested tool-result images must be preserved at their original positions or fail closed.
func ValidateGeminiPendingPreservation(ctx context.Context, original, translated []byte) error {
	f := FileRequestFromContext(ctx)
	if f == nil || !f.Local {
		return nil
	}
	source, err := ImageFileIDs(original)
	if err != nil {
		return err
	}
	target, err := ImageFileIDs(translated)
	if err != nil {
		return err
	}
	counts := map[string]int{}
	for _, id := range target {
		counts[id]++
	}
	for _, id := range source {
		if _, pending := f.Pending[id]; pending {
			if counts[id] == 0 {
				return fmt.Errorf("Gemini translation cannot preserve this pending image")
			}
			counts[id]--
		}
	}
	return nil
}
