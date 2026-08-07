//go:build darwin || linux

package pairing

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

type localBackend struct{ path string }

func NewLocal(path string) *JSONStore { return &JSONStore{backend: &localBackend{path: path}} }

func (b *localBackend) Load(ctx context.Context) (state, int64, bool, error) {
	return b.load(ctx)
}

func (b *localBackend) load(ctx context.Context) (state, int64, bool, error) {
	if err := ctx.Err(); err != nil {
		return state{}, 0, false, err
	}
	data, err := os.ReadFile(b.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return newState(), 0, false, nil
		}
		return state{}, 0, false, err
	}
	var envelope struct {
		Generation int64 `json:"generation"`
		State      state `json:"state"`
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return state{}, 0, false, err
	}
	// Read version-1 files written before generation envelopes.
	if envelope.State.Version == 0 {
		if err := json.Unmarshal(data, &envelope.State); err != nil {
			return state{}, 0, false, err
		}
		envelope.Generation = 1
	}
	initState(&envelope.State)
	return envelope.State, envelope.Generation, true, nil
}

func (b *localBackend) Save(ctx context.Context, st state, generation int64, exists bool) error {
	return b.save(ctx, st, generation, exists)
}

func (b *localBackend) save(ctx context.Context, st state, generation int64, exists bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	_, current, found, err := b.load(ctx)
	if err != nil {
		return err
	}
	if found != exists || (exists && current != generation) {
		return ErrConflict
	}
	if err := os.MkdirAll(filepath.Dir(b.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(struct {
		Generation int64 `json:"generation"`
		State      state `json:"state"`
	}{generation + 1, st}, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(b.path), ".pairing-*.tmp")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err = tmp.Chmod(0o600); err == nil {
		_, err = tmp.Write(data)
	}
	if err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(name, b.path); err != nil {
		return err
	}
	if dir, openErr := os.Open(filepath.Dir(b.path)); openErr == nil {
		_ = dir.Sync()
		_ = dir.Close()
	}
	return nil
}

func (b *localBackend) Update(ctx context.Context, fn func(*state) error) error {
	if err := os.MkdirAll(filepath.Dir(b.path), 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(b.path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX); err != nil {
		return err
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN) //nolint:errcheck
	st, generation, exists, err := b.load(ctx)
	if err != nil {
		return err
	}
	initState(&st)
	if err = fn(&st); err != nil {
		return err
	}
	return b.save(ctx, st, generation, exists)
}
