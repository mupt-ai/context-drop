package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"

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

func (s *LocalStore) List(ctx context.Context, chainID string) ([]drop.Metadata, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root := filepath.Join(s.root, "drops")
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	var out []drop.Metadata
	for _, entry := range entries {
		if !entry.IsDir() || !drop.ValidID(entry.Name()) {
			continue
		}
		meta, err := s.GetMeta(ctx, entry.Name())
		if err != nil {
			if errors.Is(err, ErrNotFound) {
				continue
			}
			return nil, err
		}
		if meta.ChainID == chainID {
			out = append(out, meta)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out, nil
}

func (s *LocalStore) Delete(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dir, err := s.dropDir(id)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	return nil
}

func (s *LocalStore) dropDir(id string) (string, error) {
	if !drop.ValidID(id) {
		return "", fmt.Errorf("invalid drop id")
	}
	return filepath.Join(s.root, "drops", id), nil
}
