package storage

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"contextdrop.dev/context-drop/internal/drop"
)

type failingReader struct{}

func (failingReader) Read(p []byte) (int, error) {
	return 0, errors.New("read failed")
}

func TestLocalStorePutGetListDelete(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := NewLocal(t.TempDir())
	now := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	first := drop.Metadata{
		ID:          "ABCDEFGHIJKLMNOP",
		Filename:    "first.txt",
		ContentType: "text/plain",
		Size:        5,
		CreatedAt:   now,
		ExpiresAt:   now.Add(time.Hour),
		ChainID:     "chain-1",
	}
	second := drop.Metadata{
		ID:          "QRSTUVWXYZabcdef",
		Filename:    "second.txt",
		ContentType: "text/plain",
		Size:        6,
		CreatedAt:   now.Add(time.Minute),
		ExpiresAt:   now.Add(time.Hour),
		ChainID:     "chain-1",
	}
	otherUser := drop.Metadata{
		ID:          "fedcbaZYXWVUTSRQ",
		Filename:    "other.txt",
		ContentType: "text/plain",
		Size:        5,
		CreatedAt:   now.Add(2 * time.Minute),
		ExpiresAt:   now.Add(time.Hour),
		ChainID:     "chain-2",
	}

	for _, item := range []struct {
		meta drop.Metadata
		body string
	}{
		{meta: first, body: "first"},
		{meta: second, body: "second"},
		{meta: otherUser, body: "other"},
	} {
		if err := store.Put(ctx, item.meta, strings.NewReader(item.body)); err != nil {
			t.Fatalf("Put(%s): %v", item.meta.ID, err)
		}
	}

	got, err := store.GetMeta(ctx, first.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, first) {
		t.Fatalf("GetMeta() = %+v, want %+v", got, first)
	}

	blob, err := store.GetBlob(ctx, first)
	if err != nil {
		t.Fatal(err)
	}
	defer blob.Close()
	data, err := io.ReadAll(blob)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "first" {
		t.Fatalf("blob = %q, want first", data)
	}

	list, err := store.List(ctx, "chain-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 || list[0].ID != second.ID || list[1].ID != first.ID {
		t.Fatalf("List() = %+v, want second then first", list)
	}

	if err := store.Delete(ctx, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetMeta(ctx, first.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetMeta(deleted) error = %v, want ErrNotFound", err)
	}
	if _, err := store.GetBlob(ctx, first); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetBlob(deleted) error = %v, want ErrNotFound", err)
	}
}

func TestLocalStoreListMissingRoot(t *testing.T) {
	t.Parallel()

	got, err := NewLocal(t.TempDir()).List(context.Background(), "user")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("List() = %+v, want empty", got)
	}
}

func TestLocalStoreInvalidID(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	store := NewLocal(t.TempDir())
	meta := drop.Metadata{ID: "../bad"}

	if err := store.Put(ctx, meta, strings.NewReader("x")); err == nil {
		t.Fatal("Put() error = nil, want invalid ID error")
	}
	if _, err := store.GetMeta(ctx, meta.ID); err == nil {
		t.Fatal("GetMeta() error = nil, want invalid ID error")
	}
	if _, err := store.GetBlob(ctx, meta); err == nil {
		t.Fatal("GetBlob() error = nil, want invalid ID error")
	}
	if err := store.Delete(ctx, meta.ID); err == nil {
		t.Fatal("Delete() error = nil, want invalid ID error")
	}
}

func TestLocalStoreContextCanceled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	store := NewLocal(t.TempDir())
	meta := drop.Metadata{ID: "ABCDEFGHIJKLMNOP"}

	if err := store.Put(ctx, meta, strings.NewReader("x")); !errors.Is(err, context.Canceled) {
		t.Fatalf("Put() error = %v, want context.Canceled", err)
	}
	if _, err := store.GetMeta(ctx, meta.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("GetMeta() error = %v, want context.Canceled", err)
	}
	if _, err := store.GetBlob(ctx, meta); !errors.Is(err, context.Canceled) {
		t.Fatalf("GetBlob() error = %v, want context.Canceled", err)
	}
	if _, err := store.List(ctx, "user"); !errors.Is(err, context.Canceled) {
		t.Fatalf("List() error = %v, want context.Canceled", err)
	}
	if err := store.Delete(ctx, meta.ID); !errors.Is(err, context.Canceled) {
		t.Fatalf("Delete() error = %v, want context.Canceled", err)
	}
}

func TestLocalStoreSkipsInvalidAndMissingMetadataDuringList(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	store := NewLocal(root)
	validMissingMetaDir := filepath.Join(root, "drops", "ABCDEFGHIJKLMNOP")
	invalidDir := filepath.Join(root, "drops", "bad")
	if err := os.MkdirAll(validMissingMetaDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(invalidDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "drops", "not-dir"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := store.List(context.Background(), "user")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("List() = %+v, want empty", got)
	}
}

func TestLocalStoreReturnsCorruptMetadataError(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	id := "ABCDEFGHIJKLMNOP"
	dir := filepath.Join(root, "drops", id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, drop.MetaName), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := NewLocal(root).GetMeta(context.Background(), id)
	if err == nil {
		t.Fatal("GetMeta() error = nil, want JSON error")
	}
}

func TestLocalStorePutErrors(t *testing.T) {
	t.Parallel()

	fileRoot := filepath.Join(t.TempDir(), "file-root")
	if err := os.WriteFile(fileRoot, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := NewLocal(fileRoot).Put(context.Background(), drop.Metadata{ID: "ABCDEFGHIJKLMNOP"}, strings.NewReader("x"))
	if err == nil {
		t.Fatal("Put() error = nil, want mkdir error")
	}

	err = NewLocal(t.TempDir()).Put(context.Background(), drop.Metadata{ID: "ABCDEFGHIJKLMNOP"}, failingReader{})
	if err == nil || !strings.Contains(err.Error(), "read failed") {
		t.Fatalf("Put() error = %v, want read failed", err)
	}
}

func TestLocalStoreReadErrors(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	id := "ABCDEFGHIJKLMNOP"
	dir := filepath.Join(root, "drops", id)
	if err := os.MkdirAll(filepath.Join(dir, drop.MetaName), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := NewLocal(root).GetMeta(context.Background(), id)
	if err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("GetMeta() error = %v, want non-not-found read error", err)
	}

	if err := os.RemoveAll(filepath.Join(dir, drop.MetaName)); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, drop.BlobName), 0o700); err != nil {
		t.Fatal(err)
	}
	blob, err := NewLocal(root).GetBlob(context.Background(), drop.Metadata{ID: id})
	if err != nil {
		t.Fatal(err)
	}
	_ = blob.Close()
}

func TestLocalStoreAdditionalFileSystemErrors(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "drops"), []byte("not a dir"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewLocal(root).List(context.Background(), "chain"); err == nil {
		t.Fatal("List(file root) error = nil, want error")
	}

	id := "ABCDEFGHIJKLMNOP"
	blobDir := filepath.Join(root, "rename", "drops", id, drop.BlobName)
	if err := os.MkdirAll(blobDir, 0o700); err != nil {
		t.Fatal(err)
	}
	meta := drop.Metadata{ID: id, ChainID: "chain"}
	if err := NewLocal(filepath.Join(root, "rename")).Put(context.Background(), meta, strings.NewReader("x")); err == nil {
		t.Fatal("Put(existing blob dir) error = nil, want rename error")
	}

	fileRoot := filepath.Join(root, "file-root")
	if err := os.WriteFile(fileRoot, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := NewLocal(fileRoot).Delete(context.Background(), id); err == nil {
		t.Fatal("Delete(file root) error = nil, want error")
	}
}

func TestLocalStoreListReturnsCorruptMetadataError(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	id := "ABCDEFGHIJKLMNOP"
	dir := filepath.Join(root, "drops", id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, drop.MetaName), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := NewLocal(root).List(context.Background(), "user")
	if err == nil {
		t.Fatal("List() error = nil, want corrupt metadata error")
	}
}
