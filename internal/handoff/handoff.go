package handoff

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	MaxSummary    = 2000
	MaxNextAction = 4000
	MaxArtifacts  = 20
	MaxMetadata   = 500
)

var ErrInvalid = errors.New("invalid handoff")

type State string

const (
	StateAvailable State = "available"
	StateInspected State = "inspected"
	StateAccepting State = "accepting"
	StateAccepted  State = "accepted"
	StateRejected  State = "rejected"
	StateExpired   State = "expired"
)

type Artifact struct {
	DropID      string `json:"drop_id"`
	Filename    string `json:"filename"`
	SHA256      string `json:"sha256,omitempty"`
	Size        int64  `json:"size,omitempty"`
	ContentType string `json:"content_type,omitempty"`
}

type Handoff struct {
	ID               string     `json:"id"`
	ChainID          string     `json:"chain_id"`
	SourceMachineID  string     `json:"source_machine_id"`
	TargetMachineID  string     `json:"target_machine_id"`
	Summary          string     `json:"summary"`
	RequestedAction  string     `json:"requested_action,omitempty"`
	Repository       string     `json:"repository,omitempty"`
	BaseCommit       string     `json:"base_commit,omitempty"`
	SourceRunID      string     `json:"source_run_id,omitempty"`
	Artifacts        []Artifact `json:"artifacts,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	ExpiresAt        time.Time  `json:"expires_at"`
	RecipientState   State      `json:"recipient_state"`
	RecipientStateAt *time.Time `json:"recipient_state_at,omitempty"`
}

type Create struct {
	TargetMachineID string
	Summary         string
	RequestedAction string
	Repository      string
	BaseCommit      string
	SourceRunID     string
	Artifacts       []Artifact
	TTL             time.Duration
}

func NewID() (string, error) {
	b := make([]byte, 18)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return "hnd_" + base64.RawURLEncoding.EncodeToString(b), nil
}

func ValidateCreate(c Create) error {
	if strings.TrimSpace(c.TargetMachineID) == "" || strings.TrimSpace(c.Summary) == "" {
		return ErrInvalid
	}
	if !utf8.ValidString(c.Summary) || len(c.Summary) > MaxSummary || len(c.RequestedAction) > MaxNextAction {
		return ErrInvalid
	}
	if len(c.Repository) > MaxMetadata || len(c.BaseCommit) > MaxMetadata || len(c.SourceRunID) > MaxMetadata || len(c.Artifacts) > MaxArtifacts {
		return ErrInvalid
	}
	if c.TTL <= 0 {
		return ErrInvalid
	}
	for _, a := range c.Artifacts {
		if strings.TrimSpace(a.DropID) == "" || strings.ContainsAny(a.Filename, `/\\`) || len(a.Filename) > 255 {
			return ErrInvalid
		}
	}
	return nil
}

func EffectiveState(h Handoff, now time.Time) State {
	if !now.Before(h.ExpiresAt) {
		return StateExpired
	}
	return h.RecipientState
}
