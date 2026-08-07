package storage

import (
	"context"
	"errors"
	"io"

	"contextdrop.dev/context-drop/internal/drop"
)

var ErrNotFound = errors.New("not found")

type Store interface {
	Put(ctx context.Context, meta drop.Metadata, body io.Reader) error
	GetMeta(ctx context.Context, id string) (drop.Metadata, error)
	GetBlob(ctx context.Context, meta drop.Metadata) (io.ReadCloser, error)
	List(ctx context.Context, chainID string) ([]drop.Metadata, error)
	Delete(ctx context.Context, id string) error
}
