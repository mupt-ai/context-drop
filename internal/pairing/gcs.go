package pairing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	gcs "cloud.google.com/go/storage"
	"google.golang.org/api/googleapi"
)

type gcsBackend struct {
	client *gcs.Client
	bucket string
	object string
}

func NewGCS(ctx context.Context, bucket, prefix string) (*JSONStore, error) {
	if strings.TrimSpace(bucket) == "" {
		return nil, fmt.Errorf("gcs bucket is required")
	}
	client, err := gcs.NewClient(ctx)
	if err != nil {
		return nil, err
	}
	object := "pairing/state.json"
	if prefix = strings.Trim(prefix, "/"); prefix != "" {
		object = prefix + "/" + object
	}
	return &JSONStore{backend: &gcsBackend{client: client, bucket: bucket, object: object}}, nil
}

func (b *gcsBackend) Load(ctx context.Context) (state, int64, bool, error) {
	obj := b.client.Bucket(b.bucket).Object(b.object)
	attrs, err := obj.Attrs(ctx)
	if err != nil {
		if isGCSNotFound(err) {
			return newState(), 0, false, nil
		}
		return state{}, 0, false, err
	}
	reader, err := obj.If(gcs.Conditions{GenerationMatch: attrs.Generation}).NewReader(ctx)
	if err != nil {
		if isGCSPreconditionFailed(err) {
			return state{}, 0, false, ErrConflict
		}
		return state{}, 0, false, err
	}
	defer reader.Close()
	data, err := io.ReadAll(reader)
	if err != nil {
		return state{}, 0, false, err
	}
	var st state
	if err := json.Unmarshal(data, &st); err != nil {
		return state{}, 0, false, err
	}
	initState(&st)
	return st, attrs.Generation, true, nil
}

func (b *gcsBackend) Save(ctx context.Context, st state, generation int64, exists bool) error {
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	obj := b.client.Bucket(b.bucket).Object(b.object)
	if exists {
		obj = obj.If(gcs.Conditions{GenerationMatch: generation})
	} else {
		obj = obj.If(gcs.Conditions{DoesNotExist: true})
	}
	writer := obj.NewWriter(ctx)
	writer.ContentType = "application/json"
	if _, err := io.Copy(writer, bytes.NewReader(data)); err != nil {
		_ = writer.Close()
		return err
	}
	if err := writer.Close(); err != nil {
		if isGCSPreconditionFailed(err) {
			return ErrConflict
		}
		return err
	}
	return nil
}

func isGCSNotFound(err error) bool {
	if errors.Is(err, gcs.ErrObjectNotExist) {
		return true
	}
	var gerr *googleapi.Error
	return errors.As(err, &gerr) && gerr.Code == httpStatusNotFound
}

func isGCSPreconditionFailed(err error) bool {
	var gerr *googleapi.Error
	return errors.As(err, &gerr) && gerr.Code == httpStatusPreconditionFailed
}

const (
	httpStatusNotFound           = 404
	httpStatusPreconditionFailed = 412
)
