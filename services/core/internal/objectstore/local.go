// Package objectstore persists immutable quotation PDFs on a local filesystem root.
// It satisfies conversation.QuotePDFObjects; it never sends or publishes anything.
package objectstore

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"

	"github.com/google/uuid"
)

var (
	ErrRoot     = errors.New("object store root must be an existing absolute directory")
	ErrRef      = errors.New("object reference is invalid or outside the tenant scope")
	ErrTooLarge = errors.New("object exceeds the read limit")
	ErrConflict = errors.New("object already exists with different content")
	ErrNotFound = errors.New("object not found")
)

var refPattern = regexp.MustCompile(`^([0-9a-f-]{36})/([0-9a-f-]{36})\.pdf$`)

// Local stores objects as <root>/<tenant>/<attempt>.pdf. Names are derived only from
// parsed UUIDs, so callers cannot choose a path.
type Local struct{ root string }

func NewLocal(root string) (*Local, error) {
	if !filepath.IsAbs(root) {
		return nil, ErrRoot
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return nil, ErrRoot
	}
	return &Local{root: filepath.Clean(root)}, nil
}

// Put writes through a temporary file and renames without replacing. Repeating the
// same attempt with identical bytes is idempotent; different bytes are a conflict.
func (l *Local) Put(ctx context.Context, tenant, attempt uuid.UUID, data []byte) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if l == nil || tenant == uuid.Nil || attempt == uuid.Nil || len(data) == 0 {
		return "", ErrRef
	}
	ref := tenant.String() + "/" + attempt.String() + ".pdf"
	dir := filepath.Join(l.root, tenant.String())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	final := filepath.Join(dir, attempt.String()+".pdf")
	if existing, err := os.ReadFile(final); err == nil {
		if !bytes.Equal(existing, data) {
			return "", ErrConflict
		}
		return ref, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, ".put-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if _, err = tmp.Write(data); err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", err
	}
	// Link fails if the target appeared concurrently, so a stored object is never replaced.
	if err = os.Link(tmp.Name(), final); err != nil {
		if errors.Is(err, os.ErrExist) {
			if existing, readErr := os.ReadFile(final); readErr == nil && bytes.Equal(existing, data) {
				return ref, nil
			}
			return "", ErrConflict
		}
		return "", fmt.Errorf("publish object: %w", err)
	}
	return ref, nil
}

// Get checks the tenant scope and size before reading any content.
func (l *Local) Get(ctx context.Context, tenant uuid.UUID, ref string, limit int64) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	m := refPattern.FindStringSubmatch(ref)
	if l == nil || m == nil || limit < 1 || tenant == uuid.Nil {
		return nil, ErrRef
	}
	owner, err := uuid.Parse(m[1])
	attempt, err2 := uuid.Parse(m[2])
	if err != nil || err2 != nil || owner != tenant || owner.String() != m[1] || attempt.String() != m[2] {
		return nil, ErrRef
	}
	file, err := os.Open(filepath.Join(l.root, m[1], m[2]+".pdf"))
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, ErrRef
	}
	if info.Size() > limit {
		return nil, ErrTooLarge
	}
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, ErrTooLarge
	}
	return data, nil
}
