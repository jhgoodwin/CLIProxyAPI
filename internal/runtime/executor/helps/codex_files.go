package helps

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/tidwall/gjson"
)

const PrototypeFilePrefix = "file-cpa-"

// CodexFile retains legacy upstream fields only for old executor regression fixtures.
// Public uploads use account-neutral private disk blobs. Metadata is process-local.
type CodexFile struct {
	ID, UpstreamID, Owner, Scope, MIME                   string
	Name, Purpose, OriginalPath, VariantPath, VariantKey string
	MemoHit                                              bool
	Bytes                                                int64
	Created, Expires                                     time.Time
	Ready                                                bool
}
type CodexFileRegistry struct {
	mu    sync.Mutex
	files map[string]CodexFile
	now   func() time.Time
	root  string
}

func NewCodexFileRegistry() *CodexFileRegistry {
	return &CodexFileRegistry{files: map[string]CodexFile{}, now: time.Now}
}
func FileCallerScope(principal string) string {
	sum := sha256.Sum256([]byte(principal))
	return hex.EncodeToString(sum[:])
}
func (r *CodexFileRegistry) remove(f CodexFile) {
	// Only files created directly inside this registry's verified private directory are removed.
	for _, p := range []string{f.OriginalPath, f.VariantPath} {
		if r.root != "" && filepath.Dir(p) == r.root && p != "" {
			_ = os.Remove(p)
		}
	}
	delete(r.files, f.ID)
	if len(r.files) == 0 && r.root != "" {
		_ = os.Remove(r.root)
		r.root = ""
	}
}
func (r *CodexFileRegistry) prune(ttl time.Duration) {
	for _, f := range r.files {
		expires := f.Expires
		if expires.IsZero() {
			expires = f.Created.Add(ttl)
		}
		if !r.now().Before(expires) {
			r.remove(f)
		}
	}
}
func (r *CodexFileRegistry) Add(f CodexFile, max int, ttl time.Duration) (CodexFile, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.prune(ttl)
	if len(r.files) >= max {
		return f, fmt.Errorf("file registry is full; wait for local expiry")
	}
	f.ID = PrototypeFilePrefix + uuid.NewString()
	f.Created = r.now()
	if f.Expires.IsZero() {
		f.Expires = f.Created.Add(ttl)
	}
	r.files[f.ID] = f
	return f, nil
}

// Store validates and memoizes independently of upstream credentials. Per-registry
// capacity bounds original plus derived storage; each derived blob is <=20MiB.
func (r *CodexFileRegistry) Store(f CodexFile, data []byte, max int, ttl time.Duration) (CodexFile, error) {
	hash := sha256.Sum256(data)
	f.VariantKey = hex.EncodeToString(hash[:]) + "-" + ScreenshotPolicyVersion + "-generic-vision"
	var variant []byte
	var err error
	r.mu.Lock()
	defer r.mu.Unlock()
	r.prune(ttl)
	if len(r.files) >= max {
		return f, fmt.Errorf("file registry is full; wait for local expiry")
	}
	for _, cached := range r.files {
		if cached.VariantKey == f.VariantKey && cached.Scope == f.Scope {
			f.MemoHit = true
			variant, err = os.ReadFile(cached.VariantPath)
			if err != nil {
				return f, fmt.Errorf("attachment storage unavailable")
			}
			break
		}
	}
	if variant == nil {
		variant, _, err = NormalizeScreenshot(data)
		if err != nil {
			return f, err
		}
	}
	if r.root == "" {
		r.root, err = os.MkdirTemp("", "cpa-screenshots-")
		if err != nil {
			return f, fmt.Errorf("attachment storage unavailable")
		}
	}
	f.ID = PrototypeFilePrefix + uuid.NewString()
	f.Created = r.now()
	f.Expires = f.Created.Add(ttl)
	f.Bytes = int64(len(data))
	f.Ready = true
	f.OriginalPath = filepath.Join(r.root, f.ID+".original")
	f.VariantPath = filepath.Join(r.root, f.ID+".png")
	if err = os.WriteFile(f.OriginalPath, data, 0600); err == nil {
		err = os.WriteFile(f.VariantPath, variant, 0600)
	}
	if err != nil {
		r.remove(f)
		return f, fmt.Errorf("attachment storage unavailable")
	}
	r.files[f.ID] = f
	return f, nil
}
func (r *CodexFileRegistry) Get(id, principal string, ttl time.Duration) (CodexFile, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.prune(ttl)
	f, ok := r.files[id]
	if !ok || f.Scope != FileCallerScope(principal) || principal == "" {
		return f, fmt.Errorf("file not found")
	}
	return f, nil
}
func (r *CodexFileRegistry) Content(id, principal string, ttl time.Duration) (CodexFile, []byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.prune(ttl)
	f, ok := r.files[id]
	if !ok || principal == "" || f.Scope != FileCallerScope(principal) {
		return f, nil, fmt.Errorf("file not found")
	}
	data, err := os.ReadFile(f.OriginalPath)
	if err != nil {
		return f, nil, fmt.Errorf("file content unavailable")
	}
	return f, data, nil
}
func (r *CodexFileRegistry) List(principal string, ttl time.Duration) []CodexFile {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.prune(ttl)
	out := []CodexFile{}
	for _, f := range r.files {
		if principal != "" && f.Scope == FileCallerScope(principal) {
			out = append(out, f)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Created.Equal(out[j].Created) {
			return out[i].ID < out[j].ID
		}
		return out[i].Created.Before(out[j].Created)
	})
	return out
}
func (r *CodexFileRegistry) Delete(id, principal string, ttl time.Duration) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.prune(ttl)
	f, ok := r.files[id]
	if !ok || principal == "" || f.Scope != FileCallerScope(principal) {
		return fmt.Errorf("file not found")
	}
	r.remove(f)
	return nil
}

// ImageSlots covers the two supported Responses attachment positions, including
// structured screenshot tool outputs. Paths are reused for discovery and rewriting.
func ImageSlots(body []byte) []string {
	return discoverHostedImageSlots(body)
}
func ImageFileIDs(body []byte) ([]string, error) {
	if !json.Valid(body) {
		return nil, fmt.Errorf("invalid JSON")
	}
	var ids []string
	for _, p := range ImageSlots(body) {
		part := gjson.GetBytes(body, p)
		if !part.Get("file_id").Exists() {
			continue
		}
		id := part.Get("file_id")
		if id.Type != gjson.String || strings.TrimSpace(id.String()) == "" {
			return nil, fmt.Errorf("input_image.file_id must be a nonempty string")
		}
		if part.Get("image_url").Exists() {
			return nil, fmt.Errorf("input_image cannot combine file_id and image_url")
		}
		ids = append(ids, id.String())
	}
	return ids, nil
}

type fileRequestKey struct{}
type FileRequest struct {
	Owner   string
	Allowed map[string]bool
	Local   bool
	Pending map[string]PendingFile
	// NativeProvider is assigned only by the selected native upload adapter.
	NativeProvider string
}

func FileRequestFromContext(ctx context.Context) *FileRequest {
	f, _ := ctx.Value(fileRequestKey{}).(*FileRequest)
	return f
}
func WithFileRequest(ctx context.Context, f *FileRequest) context.Context {
	return context.WithValue(ctx, fileRequestKey{}, f)
}
func (r *CodexFileRegistry) Resolve(ctx context.Context, body []byte, principal string, ttl time.Duration) (context.Context, []byte, error) {
	return r.resolveHostedFileReferences(ctx, body, principal, ttl)
}

// ValidateFilePayload never restores image fields removed by final user rules.
func ValidateFilePayload(ctx context.Context, body []byte) error {
	return validateHostedFilePayload(ctx, body)
}
