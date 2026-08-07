package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	gcs "cloud.google.com/go/storage"
	"contextdrop.dev/context-drop/internal/drop"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/option"
)

func TestNewGCSRequiresBucket(t *testing.T) {
	t.Parallel()

	_, err := NewGCS(context.Background(), "", "")
	if err == nil || !strings.Contains(err.Error(), "gcs bucket is required") {
		t.Fatalf("NewGCS() error = %v, want bucket required", err)
	}
}

func TestGCSStoreRejectsInvalidIDBeforeClientUse(t *testing.T) {
	t.Parallel()

	store := &GCSStore{}
	meta := drop.Metadata{ID: "../bad"}

	if err := store.Put(context.Background(), meta, strings.NewReader("x")); err == nil {
		t.Fatal("Put() error = nil, want invalid ID")
	}
	if _, err := store.GetMeta(context.Background(), meta.ID); err == nil {
		t.Fatal("GetMeta() error = nil, want invalid ID")
	}
	if _, err := store.GetBlob(context.Background(), meta); err == nil {
		t.Fatal("GetBlob() error = nil, want invalid ID")
	}
	if err := store.Delete(context.Background(), meta.ID); err == nil {
		t.Fatal("Delete() error = nil, want invalid ID")
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

func TestIDFromMetaObject(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "plain", in: "drops/ABCDEFGHIJKLMNOP/meta.json", want: "ABCDEFGHIJKLMNOP"},
		{name: "prefix", in: "prefix/drops/ABCDEFGHIJKLMNOP/meta.json", want: "ABCDEFGHIJKLMNOP"},
		{name: "trim slashes", in: "/prefix/drops/ABCDEFGHIJKLMNOP/meta.json/", want: "ABCDEFGHIJKLMNOP"},
		{name: "wrong leaf", in: "drops/ABCDEFGHIJKLMNOP/blob", want: ""},
		{name: "too short", in: "ABCDEFGHIJKLMNOP/meta.json", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := idFromMetaObject(tt.in); got != tt.want {
				t.Fatalf("idFromMetaObject(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestIsGCSNotFound(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "storage sentinel", err: gcs.ErrObjectNotExist, want: true},
		{name: "wrapped sentinel", err: errors.Join(errors.New("wrapper"), gcs.ErrObjectNotExist), want: true},
		{name: "googleapi 404", err: &googleapi.Error{Code: 404}, want: true},
		{name: "googleapi 500", err: &googleapi.Error{Code: 500}, want: false},
		{name: "other", err: errors.New("boom"), want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isGCSNotFound(tt.err); got != tt.want {
				t.Fatalf("isGCSNotFound(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

type fakeGCSServer struct {
	server       *httptest.Server
	mu           sync.Mutex
	objects      map[string][]byte
	requests     []string
	uploadStatus map[string]int
	downloadCode map[string]int
	listStatus   int
	deleteStatus int
}

func newFakeGCSServer(t *testing.T) *fakeGCSServer {
	t.Helper()
	fake := &fakeGCSServer{
		objects:      make(map[string][]byte),
		uploadStatus: make(map[string]int),
		downloadCode: make(map[string]int),
	}
	fake.server = httptest.NewServer(http.HandlerFunc(fake.serveHTTP))
	return fake
}

func (f *fakeGCSServer) close() {
	f.server.Close()
}

func (f *fakeGCSServer) serveHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests = append(f.requests, r.Method+" "+r.URL.String())
	f.mu.Unlock()
	switch {
	case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/upload/storage/v1/b/bucket/o"):
		f.handleUpload(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/storage/v1/b/bucket/o":
		f.handleList(w, r)
	case r.Method == http.MethodGet && r.URL.Path == "/b/bucket/o":
		f.handleList(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/storage/v1/b/bucket/o/"):
		f.handleDownload(w, r)
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/storage/v1/b/bucket/o/"):
		f.handleDelete(w, r)
	case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/bucket/"):
		f.handleDownload(w, r)
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/bucket/"):
		f.handleDelete(w, r)
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/b/bucket/o/"):
		f.handleDelete(w, r)
	default:
		http.Error(w, "unexpected request "+r.Method+" "+r.URL.String(), http.StatusNotFound)
	}
}

func (f *fakeGCSServer) handleUpload(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" {
		http.Error(w, "missing name", http.StatusBadRequest)
		return
	}
	if status := f.uploadStatus[name]; status != 0 {
		_, _ = io.Copy(io.Discard, r.Body)
		http.Error(w, "upload failed", status)
		return
	}
	_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	reader := multipart.NewReader(r.Body, params["boundary"])
	var content []byte
	for {
		part, err := reader.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		data, err := io.ReadAll(part)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		content = data
	}
	f.mu.Lock()
	f.objects[name] = content
	f.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_, _ = fmt.Fprintf(w, `{"bucket":"bucket","name":%q}`, name)
}

func (f *fakeGCSServer) handleDownload(w http.ResponseWriter, r *http.Request) {
	name := objectNameFromPath(r.URL.Path)
	if status := f.downloadCode[name]; status != 0 {
		http.Error(w, "download failed", status)
		return
	}
	f.mu.Lock()
	data, ok := f.objects[name]
	f.mu.Unlock()
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		_, _ = fmt.Fprintf(w, `{"error":{"code":404,"message":"not found %s"}}`, name)
		return
	}
	_, _ = w.Write(data)
}

func (f *fakeGCSServer) handleList(w http.ResponseWriter, r *http.Request) {
	if f.listStatus != 0 {
		http.Error(w, "list failed", f.listStatus)
		return
	}
	prefix := r.URL.Query().Get("prefix")
	f.mu.Lock()
	var names []string
	for name := range f.objects {
		if strings.HasPrefix(name, prefix) {
			names = append(names, name)
		}
	}
	f.mu.Unlock()
	sort.Strings(names)
	type item struct {
		Name string `json:"name"`
	}
	out := struct {
		Items []item `json:"items"`
	}{Items: make([]item, 0, len(names))}
	for _, name := range names {
		out.Items = append(out.Items, item{Name: name})
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func (f *fakeGCSServer) handleDelete(w http.ResponseWriter, r *http.Request) {
	if f.deleteStatus != 0 {
		http.Error(w, "delete failed", f.deleteStatus)
		return
	}
	name := objectNameFromPath(r.URL.Path)
	f.mu.Lock()
	delete(f.objects, name)
	f.mu.Unlock()
	w.WriteHeader(http.StatusNoContent)
}

func objectNameFromPath(path string) string {
	raw := strings.TrimPrefix(path, "/storage/v1/b/bucket/o/")
	raw = strings.TrimPrefix(raw, "/b/bucket/o/")
	raw = strings.TrimPrefix(raw, "/bucket/")
	name, err := url.PathUnescape(raw)
	if err != nil {
		return raw
	}
	return name
}

func TestGCSStoreEndToEndAgainstFakeJSONAPI(t *testing.T) {
	ctx := context.Background()
	fake := newFakeGCSServer(t)
	defer fake.close()

	client, err := gcs.NewClient(ctx, option.WithEndpoint(fake.server.URL), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	store := &GCSStore{client: client, bucket: "bucket", prefix: "prefix"}
	now := time.Date(2026, 5, 23, 12, 0, 0, 0, time.UTC)
	meta := drop.Metadata{
		ID:          "ABCDEFGHIJKLMNOP",
		Filename:    "file.txt",
		ContentType: "text/plain",
		Size:        5,
		CreatedAt:   now,
		ExpiresAt:   now.Add(time.Hour),
		ChainID:     "chain-1",
	}
	other := meta
	other.ID = "QRSTUVWXYZabcdef"
	other.ChainID = "chain-2"

	if err := store.Put(ctx, meta, strings.NewReader("hello")); err != nil {
		t.Fatal(err)
	}
	if err := store.Put(ctx, other, strings.NewReader("other")); err != nil {
		t.Fatal(err)
	}

	gotMeta, err := store.GetMeta(ctx, meta.ID)
	if err != nil {
		fake.mu.Lock()
		var keys []string
		for key := range fake.objects {
			keys = append(keys, key)
		}
		requests := append([]string(nil), fake.requests...)
		fake.mu.Unlock()
		sort.Strings(keys)
		t.Fatalf("GetMeta() error = %v; stored keys = %v; requests = %v", err, keys, requests)
	}
	if gotMeta.ID != meta.ID || gotMeta.ChainID != "chain-1" {
		t.Fatalf("GetMeta() = %+v", gotMeta)
	}

	blob, err := store.GetBlob(ctx, meta)
	if err != nil {
		t.Fatal(err)
	}
	defer blob.Close()
	data, err := io.ReadAll(blob)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "hello" {
		t.Fatalf("blob = %q, want hello", data)
	}

	list, err := store.List(ctx, "chain-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != meta.ID {
		t.Fatalf("List() = %+v, want only chain-1 drop", list)
	}

	if err := store.Delete(ctx, meta.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetMeta(ctx, meta.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetMeta(deleted) error = %v, want ErrNotFound", err)
	}
	if _, err := store.GetBlob(ctx, meta); !errors.Is(err, ErrNotFound) {
		t.Fatalf("GetBlob(deleted) error = %v, want ErrNotFound", err)
	}
	if err := store.Delete(ctx, meta.ID); err != nil {
		t.Fatalf("Delete(missing) error = %v, want nil", err)
	}
}

func TestNewGCSUsesEmulator(t *testing.T) {
	ctx := context.Background()
	fake := newFakeGCSServer(t)
	defer fake.close()
	t.Setenv("STORAGE_EMULATOR_HOST", fake.server.URL)

	store, err := NewGCS(ctx, "bucket", "/prefix/")
	if err != nil {
		t.Fatal(err)
	}
	defer store.client.Close()
	if store.bucket != "bucket" || store.prefix != "prefix" {
		t.Fatalf("NewGCS() = %+v", store)
	}
}

func TestGCSStoreErrorPaths(t *testing.T) {
	ctx := context.Background()
	fake := newFakeGCSServer(t)
	defer fake.close()
	client, err := gcs.NewClient(ctx, option.WithEndpoint(fake.server.URL), option.WithoutAuthentication())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	store := &GCSStore{client: client, bucket: "bucket", prefix: "prefix"}
	meta := drop.Metadata{ID: "ABCDEFGHIJKLMNOP", ExpiresAt: time.Now().Add(time.Hour), ChainID: "chain-1"}

	blobName := store.objectName(meta.ID, drop.BlobName)
	if err := store.Put(ctx, meta, failingReader{}); err == nil || !strings.Contains(err.Error(), "read failed") {
		t.Fatalf("Put(body read failure) error = %v, want read failed", err)
	}
	fake.uploadStatus[blobName] = http.StatusBadRequest
	if err := store.Put(ctx, meta, strings.NewReader("hello")); err == nil {
		t.Fatal("Put(blob upload failure) error = nil, want error")
	}
	delete(fake.uploadStatus, blobName)
	metaName := store.objectName(meta.ID, drop.MetaName)
	fake.uploadStatus[metaName] = http.StatusBadRequest
	if err := store.Put(ctx, meta, strings.NewReader("hello")); err == nil {
		t.Fatal("Put(meta upload failure) error = nil, want error")
	}
	delete(fake.uploadStatus, metaName)

	fake.objects[metaName] = []byte("{")
	if _, err := store.GetMeta(ctx, meta.ID); err == nil {
		t.Fatal("GetMeta(invalid JSON) error = nil, want error")
	}
	fake.downloadCode[metaName] = http.StatusBadRequest
	if _, err := store.GetMeta(ctx, meta.ID); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("GetMeta(download failure) error = %v, want non-not-found error", err)
	}
	delete(fake.downloadCode, metaName)

	fake.downloadCode[blobName] = http.StatusBadRequest
	if _, err := store.GetBlob(ctx, meta); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("GetBlob(download failure) error = %v, want non-not-found error", err)
	}
	delete(fake.downloadCode, blobName)

	fake.listStatus = http.StatusBadRequest
	if _, err := store.List(ctx, "chain-1"); err == nil {
		t.Fatal("List(server failure) error = nil, want error")
	}
	fake.listStatus = 0
	missingMetaName := store.objectName("aaaaaaaaaaaaaaaa", drop.MetaName)
	failingMetaName := store.objectName("bbbbbbbbbbbbbbbb", drop.MetaName)
	fake.objects = map[string][]byte{
		store.objectName("QRSTUVWXYZabcdef", drop.MetaName): []byte(`{"id":"QRSTUVWXYZabcdef","chain_id":"other"}`),
		"prefix/drops/not-metadata/blob":                    []byte("blob"),
		"prefix/drops/bad!/meta.json":                       []byte("{}"),
		missingMetaName:                                     []byte(`{"id":"aaaaaaaaaaaaaaaa","chain_id":"chain-1"}`),
		store.objectName(meta.ID, drop.MetaName):            []byte(`{"id":"ABCDEFGHIJKLMNOP","chain_id":"chain-1","created_at":"2026-05-23T12:00:00Z"}`),
	}
	fake.downloadCode[missingMetaName] = http.StatusNotFound
	list, err := store.List(ctx, "chain-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != meta.ID {
		t.Fatalf("List() = %+v, want chain-1 drop", list)
	}
	delete(fake.downloadCode, missingMetaName)
	fake.objects = map[string][]byte{failingMetaName: []byte(`{"id":"bbbbbbbbbbbbbbbb","chain_id":"chain-1"}`)}
	fake.downloadCode[failingMetaName] = http.StatusBadRequest
	if _, err := store.List(ctx, "chain-1"); err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("List(meta download failure) error = %v, want non-not-found error", err)
	}
	delete(fake.downloadCode, failingMetaName)

	fake.deleteStatus = http.StatusNotFound
	if err := store.Delete(ctx, meta.ID); err != nil {
		t.Fatalf("Delete(not found) error = %v, want nil", err)
	}
	fake.deleteStatus = http.StatusBadRequest
	if err := store.Delete(ctx, meta.ID); err == nil {
		t.Fatal("Delete(server failure) error = nil, want error")
	}
}
