package storage

import (
	"bytes"
	"context"
	"errors"
	"io"
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
