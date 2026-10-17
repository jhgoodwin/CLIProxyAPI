package helps

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// NativeFilesClient copies the selected proxy transport and refuses every redirect.
// Upload URLs and credentials must never be logged or forwarded to redirect targets.
func NativeFilesClient(client *http.Client) *http.Client {
	clone := *client
	clone.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &clone
}

func nativeFileResponse(ctx context.Context, client *http.Client, req *http.Request, output any) (http.Header, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("native Files transport failed")
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			LogWithRequestID(ctx).Debug("native Files response close failed")
		}
	}()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("native Files request failed (HTTP %d)", resp.StatusCode)
	}
	if output != nil {
		data, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
		if err != nil || len(data) > 1<<20 || json.Unmarshal(data, output) != nil {
			return nil, fmt.Errorf("invalid native Files response")
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return resp.Header.Clone(), nil
}
