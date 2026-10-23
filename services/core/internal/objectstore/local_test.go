package objectstore

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/google/uuid"
)

func TestLocalRoundTripAndImmutability(t *testing.T) {
	store, err := NewLocal(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	tenant, attempt := uuid.New(), uuid.New()
	data := []byte("%PDF-1.7 synthetic")
	ref, err := store.Put(t.Context(), tenant, attempt, data)
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.Get(t.Context(), tenant, ref, 1<<20)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("get = %q, %v", got, err)
	}
	if again, err := store.Put(t.Context(), tenant, attempt, data); err != nil || again != ref {
		t.Fatalf("identical retry = %q, %v", again, err)
	}
	if _, err := store.Put(t.Context(), tenant, attempt, []byte("%PDF-other")); !errors.Is(err, ErrConflict) {
		t.Fatalf("different bytes = %v", err)
	}
	if got, _ := store.Get(t.Context(), tenant, ref, 1<<20); !bytes.Equal(got, data) {
		t.Fatal("stored object was replaced")
	}
}

func TestLocalRejectsCrossTenantTraversalAndOversize(t *testing.T) {
	root := t.TempDir()
	store, _ := NewLocal(root)
	tenant, attempt := uuid.New(), uuid.New()
	ref, err := store.Put(t.Context(), tenant, attempt, []byte("%PDF-1.7 synthetic"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(t.Context(), uuid.New(), ref, 1<<20); !errors.Is(err, ErrRef) {
		t.Fatalf("cross tenant = %v", err)
	}
	for _, bad := range []string{"", "../x", tenant.String() + "/../" + attempt.String() + ".pdf", filepath.Join(root, "x.pdf"), "synthetic-pdf", ref + "/", tenant.String() + "/" + attempt.String() + ".PDF"} {
		if _, err := store.Get(t.Context(), tenant, bad, 1<<20); !errors.Is(err, ErrRef) {
			t.Fatalf("ref %q = %v", bad, err)
		}
	}
	if _, err := store.Get(t.Context(), tenant, ref, 4); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("oversize = %v", err)
	}
	if _, err := store.Get(t.Context(), tenant, tenant.String()+"/"+uuid.NewString()+".pdf", 1<<20); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing = %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := store.Put(ctx, tenant, uuid.New(), []byte("x")); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled put = %v", err)
	}
}

func TestLocalRootMustBeExistingAbsoluteDirectory(t *testing.T) {
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{"", "relative", filepath.Join(t.TempDir(), "missing"), file} {
		if _, err := NewLocal(root); !errors.Is(err, ErrRoot) {
			t.Fatalf("root %q = %v", root, err)
		}
	}
}

func TestLocalConcurrentSameAttemptStoresOnce(t *testing.T) {
	store, _ := NewLocal(t.TempDir())
	tenant, attempt := uuid.New(), uuid.New()
	var wg sync.WaitGroup
	results := make([]error, 8)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			data := []byte("%PDF-1.7 same")
			if i%2 == 1 {
				data = []byte("%PDF-1.7 diff")
			}
			_, results[i] = store.Put(t.Context(), tenant, attempt, data)
		}()
	}
	wg.Wait()
	ok, conflict := 0, 0
	for _, err := range results {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, ErrConflict):
			conflict++
		default:
			t.Fatal(err)
		}
	}
	if ok == 0 || ok+conflict != len(results) {
		t.Fatalf("ok=%d conflict=%d", ok, conflict)
	}
	entries, _ := os.ReadDir(filepath.Join(store.root, tenant.String()))
	if len(entries) != 1 {
		t.Fatalf("directory has %d entries (temporary files must not remain)", len(entries))
	}
}
