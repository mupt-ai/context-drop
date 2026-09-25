package storage

import (
	"context"
	"errors"
	"strings"
	"testing"

	gcs "cloud.google.com/go/storage"
	"contextdrop.dev/context-drop/internal/drop"
	"google.golang.org/api/googleapi"
)

func TestNewGCSRequiresBucket(t *testing.T) {
	t.Parallel()
	_, err := NewGCS(context.Background(), "", "")
	if err == nil || !strings.Contains(err.Error(), "gcs bucket is required") {
		t.Fatalf("NewGCS() error = %v", err)
	}
}

func TestGCSStoreRejectsInvalidIDBeforeClientUse(t *testing.T) {
	t.Parallel()
	store := &GCSStore{}
	meta := drop.Metadata{ID: "../bad"}
	if err := store.Put(context.Background(), meta, strings.NewReader("x")); err == nil {
		t.Fatal("Put() error = nil")
	}
	if _, err := store.GetMeta(context.Background(), meta.ID); err == nil {
		t.Fatal("GetMeta() error = nil")
	}
	if _, err := store.GetBlob(context.Background(), meta); err == nil {
		t.Fatal("GetBlob() error = nil")
	}
}

func TestGCSObjectName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		store  GCSStore
		id     string
		object string
		want   string
	}{
		{name: "no prefix root", store: GCSStore{}, want: "drops/"},
		{name: "no prefix blob", store: GCSStore{}, id: "ABCDEFGHIJKLMNOP", object: drop.BlobName, want: "drops/ABCDEFGHIJKLMNOP/blob"},
		{name: "with prefix root", store: GCSStore{prefix: "prefix"}, want: "prefix/drops/"},
		{name: "with prefix metadata", store: GCSStore{prefix: "prefix"}, id: "ABCDEFGHIJKLMNOP", object: drop.MetaName, want: "prefix/drops/ABCDEFGHIJKLMNOP/meta.json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.store.objectName(tt.id, tt.object); got != tt.want {
				t.Fatalf("objectName() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestIsGCSNotFound(t *testing.T) {
	t.Parallel()
	tests := []struct {
		err  error
		want bool
	}{
		{err: gcs.ErrObjectNotExist, want: true},
		{err: errors.Join(errors.New("wrapper"), gcs.ErrObjectNotExist), want: true},
		{err: &googleapi.Error{Code: 404}, want: true},
		{err: &googleapi.Error{Code: 500}, want: false},
		{err: errors.New("boom"), want: false},
	}
	for _, tt := range tests {
		if got := isGCSNotFound(tt.err); got != tt.want {
			t.Fatalf("isGCSNotFound(%v) = %v, want %v", tt.err, got, tt.want)
		}
	}
}
