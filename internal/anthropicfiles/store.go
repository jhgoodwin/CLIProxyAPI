// Package anthropicfiles stores original image bytes in a private, durable local store.
// A root must be owned by one Store instance; callers must not share it across processes.
package anthropicfiles

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	_ "golang.org/x/image/webp"
)

const MaxUploadBytes int64 = 128 << 20
const DefaultTTL = 7 * 24 * time.Hour
const MinTTL = time.Hour
const MaxTTL = 30 * 24 * time.Hour

var (
	ErrNotFound     = errors.New("file not found")
	ErrUnauthorized = errors.New("authenticated principal required")
	ErrQuota        = errors.New("file storage quota exceeded")
	ErrInvalid      = errors.New("invalid file request")
)

type Metadata struct {
	Purpose      string    `json:"purpose"`
	ID           string    `json:"id"`
	Type         string    `json:"type"`
	Filename     string    `json:"filename"`
	MIMEType     string    `json:"mime_type"`
	SizeBytes    int64     `json:"size_bytes"`
	CreatedAt    time.Time `json:"created_at"`
	ExpiresAt    time.Time `json:"expires_at"`
	Downloadable bool      `json:"downloadable"`
}
type record struct {
	Metadata
	Owner  string `json:"owner"`
	Digest string `json:"sha256"`
}
type ListOptions struct {
	Limit    int
	AfterID  string
	BeforeID string
}
type Page struct {
	Data    []Metadata `json:"data"`
	HasMore bool       `json:"has_more"`
	FirstID string     `json:"first_id,omitempty"`
	LastID  string     `json:"last_id,omitempty"`
}
type Option func(*Store)

func WithClock(now func() time.Time) Option {
	return func(s *Store) {
		if now != nil {
			s.now = now
		}
	}
}

type Store struct {
	mu       sync.Mutex
	root     string
	maxFiles int
	maxBytes int64
	now      func() time.Time
	records  map[string]record
	closed   bool
}

// Open recovers published directories and removes interrupted staging/deletion work.
// Nonpositive quotas use bounded defaults (1000 files and 10 GiB), not unlimited storage.
func Open(root string, maxFiles int, maxBytes int64, opts ...Option) (*Store, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("storage directory: %w", ErrInvalid)
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(abs, 0700); err != nil {
		return nil, err
	}
	fi, err := os.Lstat(abs)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("storage root must be a real directory")
	}
	if err = os.Chmod(abs, 0700); err != nil {
		return nil, err
	}
	if maxFiles <= 0 {
		maxFiles = 1000
	}
	if maxBytes <= 0 {
		maxBytes = 10 << 30
	}
	s := &Store{root: abs, maxFiles: maxFiles, maxBytes: maxBytes, now: time.Now, records: map[string]record{}}
	for _, opt := range opts {
		opt(s)
	}
	entries, err := os.ReadDir(abs)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		name := entry.Name()
		p := filepath.Join(abs, name)
		if strings.HasPrefix(name, ".stage-") || strings.HasPrefix(name, ".trash-") {
			if err = os.RemoveAll(p); err != nil {
				return nil, err
			}
			continue
		}
		if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("unexpected storage entry %q", name)
		}
		if err = os.Chmod(p, 0700); err != nil {
			return nil, err
		}
		metadata, err := readPrivate(filepath.Join(p, "metadata.json"))
		if err != nil {
			return nil, err
		}
		var r record
		if err = json.Unmarshal(metadata, &r); err != nil {
			return nil, fmt.Errorf("recover metadata: %w", err)
		}
		if !validID(name, r.Owner) || r.ID != name || r.Type != "file" || r.ExpiresAt.Before(r.CreatedAt) || r.SizeBytes <= 0 || r.SizeBytes > MaxUploadBytes {
			return nil, fmt.Errorf("invalid published file %q", name)
		}
		data, err := readPrivate(filepath.Join(p, "content"))
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(data)
		if int64(len(data)) != r.SizeBytes || hex.EncodeToString(sum[:]) != r.Digest {
			return nil, fmt.Errorf("corrupt published file %q", name)
		}
		if r.Purpose == "" {
			r.Purpose = "user_data"
		}
		if r.Purpose != "vision" && r.Purpose != "user_data" {
			return nil, fmt.Errorf("invalid published purpose %q", name)
		}
		s.records[name] = r
	}
	if err = s.cleanupLocked(); err != nil {
		return nil, err
	}
	if err = syncDir(abs); err != nil {
		return nil, err
	}
	return s, nil
}
func readPrivate(path string) ([]byte, error) {
	fi, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("nonregular storage file")
	}
	if err = os.Chmod(path, 0600); err != nil {
		return nil, err
	}
	return os.ReadFile(path)
}

// PrincipalHash returns opaque ownership and usage attribution, without exposing credentials.
func PrincipalHash(principal string) string {
	sum := sha256.Sum256([]byte(principal))
	return hex.EncodeToString(sum[:])
}
func owner(principal string) string { return PrincipalHash(principal) }
func validID(id, hash string) bool {
	if len(hash) != 64 || len(id) != 5+64+1+16+1+32 || !strings.HasPrefix(id, "file_"+hash+"_") {
		return false
	}
	_, err := hex.DecodeString(strings.ReplaceAll(id[5:], "_", ""))
	return err == nil
}
func (s *Store) check(principal string) error {
	if strings.TrimSpace(principal) == "" {
		return ErrUnauthorized
	}
	if s.closed {
		return errors.New("file store closed")
	}
	return s.cleanupLocked()
}

// ValidateImage fully decodes supported images, including every GIF frame. A pixel
// budget bounds decompression memory independently of the compressed upload limit.
func ValidateImage(data []byte, declaredMIME ...string) (string, error) {
	if len(data) == 0 || int64(len(data)) > MaxUploadBytes {
		return "", ErrInvalid
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > 40_000_000 {
		return "", fmt.Errorf("invalid or oversized image: %w", ErrInvalid)
	}
	mime := map[string]string{"jpeg": "image/jpeg", "png": "image/png", "gif": "image/gif", "webp": "image/webp"}[format]
	if mime == "" {
		return "", ErrInvalid
	}
	if format == "gif" {
		if err = gifBudget(data); err != nil {
			return "", err
		}
		g, e := gif.DecodeAll(bytes.NewReader(data))
		err = e
		if e == nil {
			var pixels int64
			for _, frame := range g.Image {
				pixels += int64(frame.Bounds().Dx()) * int64(frame.Bounds().Dy())
				if pixels > 40_000_000 {
					return "", ErrInvalid
				}
			}
		}
	} else {
		_, _, err = image.Decode(bytes.NewReader(data))
	}
	if err != nil {
		return "", fmt.Errorf("malformed image: %w", ErrInvalid)
	}
	if len(declaredMIME) > 0 && declaredMIME[0] != mime {
		return "", fmt.Errorf("MIME does not match image: %w", ErrInvalid)
	}
	return mime, nil
}

// gifBudget parses frame dimensions before DecodeAll allocates every frame.
func gifBudget(data []byte) error {
	if len(data) < 13 {
		return ErrInvalid
	}
	pos := 13
	if data[10]&0x80 != 0 {
		pos += 3 * (1 << ((data[10] & 7) + 1))
	}
	skip := func() bool {
		for {
			if pos >= len(data) {
				return false
			}
			n := int(data[pos])
			pos++
			if n == 0 {
				return true
			}
			if n > len(data)-pos {
				return false
			}
			pos += n
		}
	}
	var pixels int64
	for pos < len(data) {
		block := data[pos]
		pos++
		switch block {
		case 0x3b:
			return nil
		case 0x21:
			if pos >= len(data) {
				return ErrInvalid
			}
			pos++
			if !skip() {
				return ErrInvalid
			}
		case 0x2c:
			if len(data)-pos < 9 {
				return ErrInvalid
			}
			w := int64(binary.LittleEndian.Uint16(data[pos+4:]))
			h := int64(binary.LittleEndian.Uint16(data[pos+6:]))
			pixels += w * h
			if pixels > 40_000_000 {
				return ErrInvalid
			}
			packed := data[pos+8]
			pos += 9
			if packed&0x80 != 0 {
				pos += 3 * (1 << ((packed & 7) + 1))
			}
			if pos >= len(data) {
				return ErrInvalid
			}
			pos++
			if !skip() {
				return ErrInvalid
			}
		default:
			return ErrInvalid
		}
	}
	return ErrInvalid
}
func (s *Store) Put(principal, filename, mime string, data []byte, ttl time.Duration) (Metadata, error) {
	return s.PutWithPurpose(principal, filename, mime, "user_data", data, ttl)
}

// PutWithPurpose persists the original bytes and OpenAI-compatible purpose together.
func (s *Store) PutWithPurpose(principal, filename, mime, purpose string, data []byte, ttl time.Duration) (Metadata, error) {
	if strings.TrimSpace(principal) == "" {
		return Metadata{}, ErrUnauthorized
	}
	if purpose != "vision" && purpose != "user_data" {
		return Metadata{}, ErrInvalid
	}
	if ttl == 0 {
		ttl = DefaultTTL
	}
	if ttl < 0 || ttl > MaxTTL || filename == "" || len(filename) > 4096 || strings.ContainsAny(filename, "\x00\r\n") {
		return Metadata{}, ErrInvalid
	}
	actual, err := ValidateImage(data)
	if err != nil {
		return Metadata{}, err
	}
	if mime != actual {
		return Metadata{}, fmt.Errorf("MIME does not match image: %w", ErrInvalid)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err = s.check(principal); err != nil {
		return Metadata{}, err
	}
	var used int64
	for _, r := range s.records {
		used += r.SizeBytes
	}
	if len(s.records) >= s.maxFiles || int64(len(data)) > s.maxBytes-used {
		return Metadata{}, ErrQuota
	}
	now := s.now().UTC()
	var nonce [16]byte
	if _, err = rand.Read(nonce[:]); err != nil {
		return Metadata{}, err
	}
	hash := owner(principal)
	id := fmt.Sprintf("file_%s_%016x_%x", hash, now.UnixNano(), nonce)
	m := Metadata{Purpose: purpose, ID: id, Type: "file", Filename: filename, MIMEType: mime, SizeBytes: int64(len(data)), CreatedAt: now, ExpiresAt: now.Add(ttl), Downloadable: true}
	sum := sha256.Sum256(data)
	r := record{Metadata: m, Owner: hash, Digest: hex.EncodeToString(sum[:])}
	encoded, err := json.Marshal(r)
	if err != nil {
		return Metadata{}, err
	}
	stage, err := os.MkdirTemp(s.root, ".stage-")
	if err != nil {
		return Metadata{}, err
	}
	defer func() { _ = os.RemoveAll(stage) }()
	if err = writeSync(filepath.Join(stage, "content"), data); err != nil {
		return Metadata{}, err
	}
	if err = writeSync(filepath.Join(stage, "metadata.json"), encoded); err != nil {
		return Metadata{}, err
	}
	if err = syncDir(stage); err != nil {
		return Metadata{}, err
	}
	if err = os.Rename(stage, filepath.Join(s.root, id)); err != nil {
		return Metadata{}, err
	}
	s.records[id] = r
	if err = syncDir(s.root); err != nil {
		return Metadata{}, fmt.Errorf("publication durability uncertain: %w", err)
	}
	return m, nil
}
func writeSync(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	return err
}
func syncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	err = f.Sync()
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	return err
}
func (s *Store) lookup(principal, id string) (record, error) {
	if err := s.check(principal); err != nil {
		return record{}, err
	}
	r, ok := s.records[id]
	if !ok || r.Owner != owner(principal) {
		return record{}, ErrNotFound
	}
	return r, nil
}
func (s *Store) Get(principal, id string) (Metadata, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.lookup(principal, id)
	return r.Metadata, err
}
func (s *Store) Content(principal, id string) (Metadata, []byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.lookup(principal, id)
	if err != nil {
		return Metadata{}, nil, err
	}
	data, err := readPrivate(filepath.Join(s.root, id, "content"))
	if err != nil {
		return Metadata{}, nil, err
	}
	sum := sha256.Sum256(data)
	if int64(len(data)) != r.SizeBytes || hex.EncodeToString(sum[:]) != r.Digest {
		return Metadata{}, nil, errors.New("file integrity failure")
	}
	return r.Metadata, data, nil
}
func (s *Store) Delete(principal, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.lookup(principal, id); err != nil {
		return err
	}
	return s.removeLocked(id)
}
func (s *Store) removeLocked(id string) error {
	trash := filepath.Join(s.root, ".trash-"+id)
	if err := os.Rename(filepath.Join(s.root, id), trash); err != nil {
		return err
	}
	delete(s.records, id)
	if err := syncDir(s.root); err != nil {
		return err
	}
	if err := os.RemoveAll(trash); err != nil {
		return err
	}
	return syncDir(s.root)
}
func (s *Store) cleanupLocked() error {
	now := s.now()
	for id, r := range s.records {
		if !now.Before(r.ExpiresAt) {
			if err := s.removeLocked(id); err != nil {
				return err
			}
		}
	}
	return nil
}
func (s *Store) Cleanup() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("file store closed")
	}
	return s.cleanupLocked()
}
func (s *Store) Close() error { s.mu.Lock(); defer s.mu.Unlock(); s.closed = true; return nil }
func (s *Store) List(principal string, opt ListOptions) (Page, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.check(principal); err != nil {
		return Page{}, err
	}
	hash := owner(principal)
	if opt.Limit == 0 {
		opt.Limit = 20
	}
	if opt.Limit < 1 || opt.Limit > 1000 || (opt.AfterID != "" && opt.BeforeID != "") {
		return Page{}, ErrInvalid
	}
	for _, cursor := range []string{opt.AfterID, opt.BeforeID} {
		if cursor != "" && !validID(cursor, hash) {
			return Page{}, ErrInvalid
		}
	}
	ids := make([]string, 0)
	for id, r := range s.records {
		if r.Owner == hash && (opt.AfterID == "" || id < opt.AfterID) && (opt.BeforeID == "" || id > opt.BeforeID) {
			ids = append(ids, id)
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(ids)))
	page := Page{Data: []Metadata{}, HasMore: len(ids) > opt.Limit}
	if len(ids) > opt.Limit {
		if opt.BeforeID != "" {
			ids = ids[len(ids)-opt.Limit:]
		} else {
			ids = ids[:opt.Limit]
		}
	}
	for _, id := range ids {
		page.Data = append(page.Data, s.records[id].Metadata)
	}
	if len(ids) > 0 {
		page.FirstID = ids[0]
		page.LastID = ids[len(ids)-1]
	}
	return page, nil
}

// ReadUpload bounds allocation even for streams with no advertised content length.
func ReadUpload(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxUploadBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > MaxUploadBytes {
		return nil, ErrInvalid
	}
	return data, nil
}
