package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"contextdrop.dev/context-drop/internal/drop"
)

type LocalStore struct {
	root string
}

func NewLocal(root string) *LocalStore {
	return &LocalStore{root: root}
}

func (s *LocalStore) Put(ctx context.Context, meta drop.Metadata, body io.Reader) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dir, err := s.dropDir(meta.ID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}

	blobTmp := filepath.Join(dir, drop.BlobName+".tmp")
	blob, err := os.OpenFile(blobTmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(blob, body)
	closeErr := blob.Close()
	if copyErr != nil {
		_ = os.Remove(blobTmp)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(blobTmp)
		return closeErr
	}
	if err := os.Rename(blobTmp, filepath.Join(dir, drop.BlobName)); err != nil {
		_ = os.Remove(blobTmp)
		return err
	}

	metaTmp := filepath.Join(dir, drop.MetaName+".tmp")
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(metaTmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(metaTmp, filepath.Join(dir, drop.MetaName))
}

func (s *LocalStore) GetMeta(ctx context.Context, id string) (drop.Metadata, error) {
	if err := ctx.Err(); err != nil {
		return drop.Metadata{}, err
	}
	dir, err := s.dropDir(id)
	if err != nil {
		return drop.Metadata{}, err
	}
	data, err := os.ReadFile(filepath.Join(dir, drop.MetaName))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return drop.Metadata{}, ErrNotFound
		}
		return drop.Metadata{}, err
	}
	var meta drop.Metadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return drop.Metadata{}, err
	}
	return meta, nil
}

func (s *LocalStore) GetBlob(ctx context.Context, meta drop.Metadata) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dir, err := s.dropDir(meta.ID)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(filepath.Join(dir, drop.BlobName))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return f, nil
}

func (s *LocalStore) dropDir(id string) (string, error) {
	if !drop.ValidID(id) {
		return "", fmt.Errorf("invalid drop id")
	}
	return filepath.Join(s.root, "drops", id), nil
}

// DeleteExpired removes drops whose expiry is before now and returns how many it removed.
func (s *LocalStore) DeleteExpired(ctx context.Context, now time.Time) (int, error) {
	entries, err := os.ReadDir(filepath.Join(s.root, "drops"))
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return removed, err
		}
		if !e.IsDir() || !drop.ValidID(e.Name()) {
			continue
		}
		meta, err := s.GetMeta(ctx, e.Name())
		switch {
		case errors.Is(err, ErrNotFound):
			// A Put that failed before writing metadata; give an in-flight one a day.
			if info, err := e.Info(); err != nil || now.Sub(info.ModTime()) < 24*time.Hour {
				continue
			}
		case err != nil || meta.ExpiresAt.IsZero() || meta.ExpiresAt.After(now):
			continue
		}
		if err := os.RemoveAll(filepath.Join(s.root, "drops", e.Name())); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}
