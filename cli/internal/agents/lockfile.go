package agents

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// updateJSON reads the JSON value in path into v under an exclusive lock on
// path+".lock", lets fn change it, and, when fn reports a change, writes it
// back atomically. A missing file leaves v as it is.
func updateJSON(path string, v any, fn func() (changed bool, err error)) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	lock, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN) //nolint:errcheck

	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, v); err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	changed, err := fn()
	if err != nil || !changed {
		return err
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
