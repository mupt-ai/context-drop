//go:build darwin || linux

package runtimeclient

import (
	"os"

	"golang.org/x/sys/unix"
)

type configLock struct{ file *os.File }

func lockConfig(path string) (*configLock, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX); err != nil {
		file.Close()
		return nil, err
	}
	return &configLock{file: file}, nil
}

func (l *configLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	_ = unix.Flock(int(l.file.Fd()), unix.LOCK_UN)
	return l.file.Close()
}
