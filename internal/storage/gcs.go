package storage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"cloud.google.com/go/storage"
	"contextdrop.dev/context-drop/internal/drop"
	"google.golang.org/api/googleapi"
)

type GCSStore struct {
	client *storage.Client
	bucket string
	prefix string
}

func NewGCS(ctx context.Context, bucket, prefix string) (*GCSStore, error) {
	if bucket == "" {
		return nil, fmt.Errorf("gcs bucket is required")
	}
	client, err := storage.NewClient(ctx)
	if err != nil {
		return nil, err
	}
	return &GCSStore{client: client, bucket: bucket, prefix: strings.Trim(prefix, "/")}, nil
}

func (s *GCSStore) Put(ctx context.Context, meta drop.Metadata, body io.Reader) error {
	if !drop.ValidID(meta.ID) {
		return fmt.Errorf("invalid drop id")
	}
	bucket := s.client.Bucket(s.bucket)

	blobWriter := bucket.Object(s.objectName(meta.ID, drop.BlobName)).NewWriter(ctx)
	blobWriter.ContentType = meta.ContentType
	blobWriter.Metadata = map[string]string{
		"filename":   meta.Filename,
		"expires_at": meta.ExpiresAt.UTC().Format(timeFormatRFC3339Nano),
	}
	if _, err := io.Copy(blobWriter, body); err != nil {
		_ = blobWriter.Close()
		return err
	}
	if err := blobWriter.Close(); err != nil {
		return err
	}

	data, err := json.Marshal(meta)
	if err != nil {
		return err
	}
	metaWriter := bucket.Object(s.objectName(meta.ID, drop.MetaName)).NewWriter(ctx)
	metaWriter.ContentType = "application/json"
	if _, err := io.Copy(metaWriter, bytes.NewReader(data)); err != nil {
		_ = metaWriter.Close()
		return err
	}
	return metaWriter.Close()
}

func (s *GCSStore) GetMeta(ctx context.Context, id string) (drop.Metadata, error) {
	if !drop.ValidID(id) {
		return drop.Metadata{}, fmt.Errorf("invalid drop id")
	}
	r, err := s.client.Bucket(s.bucket).Object(s.objectName(id, drop.MetaName)).NewReader(ctx)
	if err != nil {
		if isGCSNotFound(err) {
			return drop.Metadata{}, ErrNotFound
		}
		return drop.Metadata{}, err
	}
	defer r.Close()
	var meta drop.Metadata
	if err := json.NewDecoder(r).Decode(&meta); err != nil {
		return drop.Metadata{}, err
	}
	return meta, nil
}

func (s *GCSStore) GetBlob(ctx context.Context, meta drop.Metadata) (io.ReadCloser, error) {
	if !drop.ValidID(meta.ID) {
		return nil, fmt.Errorf("invalid drop id")
	}
	r, err := s.client.Bucket(s.bucket).Object(s.objectName(meta.ID, drop.BlobName)).NewReader(ctx)
	if err != nil {
		if isGCSNotFound(err) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return r, nil
}

func (s *GCSStore) objectName(id, name string) string {
	path := "drops/"
	if id != "" {
		path += id + "/"
	}
	path += name
	if s.prefix == "" {
		return path
	}
	return s.prefix + "/" + path
}

func isGCSNotFound(err error) bool {
	if errors.Is(err, storage.ErrObjectNotExist) {
		return true
	}
	var gerr *googleapi.Error
	return errors.As(err, &gerr) && gerr.Code == 404
}

const timeFormatRFC3339Nano = "2006-01-02T15:04:05.999999999Z07:00"
