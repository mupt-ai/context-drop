package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"contextdrop.dev/context-drop/internal/drop"
)

func TestLocalStorePutAndGet(t *testing.T) {
	store := NewLocal(t.TempDir())
	meta := drop.Metadata{ID: "ABCDEFGHIJKLMNOP", Filename: "hello.txt", ContentType: "text/plain", Size: 5, CreatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour)}
	if err := store.Put(context.Background(), meta, bytes.NewBufferString("hello")); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetMeta(context.Background(), meta.ID)
	if err != nil || got.ID != meta.ID || got.Filename != meta.Filename {
		t.Fatalf("GetMeta() = %+v, %v", got, err)
	}
	blob, err := store.GetBlob(context.Background(), got)
	if err != nil {
		t.Fatal(err)
	}
	defer blob.Close()
	data, err := io.ReadAll(blob)
	if err != nil || string(data) != "hello" {
		t.Fatalf("GetBlob() = %q, %v", data, err)
	}
}

func TestLocalStoreErrors(t *testing.T) {
	store := NewLocal(t.TempDir())
	if _, err := store.GetMeta(context.Background(), "bad"); err == nil {
		t.Fatal("GetMeta(invalid) error = nil")
	}
	if _, err := store.GetMeta(context.Background(), "ABCDEFGHIJKLMNOP"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetMeta(missing) = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.Put(ctx, drop.Metadata{ID: "ABCDEFGHIJKLMNOP"}, bytes.NewReader(nil)); !errors.Is(err, context.Canceled) {
		t.Fatalf("Put(canceled) = %v", err)
	}
}

func TestLocalDeleteExpired(t *testing.T) {
	ctx := context.Background()
	store := NewLocal(t.TempDir())
	now := time.Now().UTC()
	put := func(id string, expires time.Time) {
		t.Helper()
		meta := drop.Metadata{ID: id, ExpiresAt: expires}
		if err := store.Put(ctx, meta, strings.NewReader("x")); err != nil {
			t.Fatal(err)
		}
	}
	oldID, _ := drop.NewID()
	liveID, _ := drop.NewID()
	put(oldID, now.Add(-time.Minute))
	put(liveID, now.Add(time.Hour))

	n, err := store.DeleteExpired(ctx, now)
	if err != nil || n != 1 {
		t.Fatalf("DeleteExpired = %d, %v; want 1", n, err)
	}
	if _, err := store.GetMeta(ctx, oldID); err != ErrNotFound {
		t.Fatalf("expired drop still readable: %v", err)
	}
	if _, err := store.GetMeta(ctx, liveID); err != nil {
		t.Fatalf("live drop removed: %v", err)
	}
}

func TestLocalDeleteExpiredRemovesStaleHalfWrittenDrops(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	store := NewLocal(root)
	id, _ := drop.NewID()
	dir := filepath.Join(root, "drops", id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, drop.BlobName), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if n, _ := store.DeleteExpired(ctx, time.Now()); n != 0 {
		t.Fatal("removed a drop that may still be uploading")
	}
	if n, err := store.DeleteExpired(ctx, time.Now().Add(25*time.Hour)); err != nil || n != 1 {
		t.Fatalf("DeleteExpired = %d, %v; want the stale half-written drop removed", n, err)
	}
}
