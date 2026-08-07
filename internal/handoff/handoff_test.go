package handoff

import (
	"testing"
	"time"
)

func TestValidateCreate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		input   Create
		wantErr bool
	}{
		{"valid", Create{TargetMachineID: "mach", Summary: "summary", TTL: time.Hour}, false},
		{"empty summary", Create{TargetMachineID: "mach", TTL: time.Hour}, true},
		{"unsafe artifact name", Create{TargetMachineID: "mach", Summary: "x", TTL: time.Hour, Artifacts: []Artifact{{DropID: "id", Filename: "../x"}}}, true},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateCreate(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestEffectiveStateExpiresLocally(t *testing.T) {
	t.Parallel()
	now := time.Now()
	h := Handoff{RecipientState: StateAvailable, ExpiresAt: now.Add(-time.Second)}
	if got := EffectiveState(h, now); got != StateExpired {
		t.Fatalf("got %s", got)
	}
}
