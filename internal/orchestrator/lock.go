package orchestrator

import "errors"

var ErrLocked = errors.New("another Context Drop daemon tick is active")
