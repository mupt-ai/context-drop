package drop

import (
	"crypto/rand"
	"encoding/base64"
	"regexp"
	"strings"
	"time"
)

const (
	BlobName = "blob"
	MetaName = "meta.json"
)

var safeIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{16,64}$`)

// Metadata describes one uploaded drop.
type Metadata struct {
	ID          string    `json:"id"`
	ObjectKey   string    `json:"object_key"`
	Filename    string    `json:"filename"`
	ContentType string    `json:"content_type"`
	Size        int64     `json:"size"`
	SHA256      string    `json:"sha256,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
	ExpiresAt   time.Time `json:"expires_at"`
	ChainID     string    `json:"chain_id,omitempty"`
}

// NewID returns a URL-safe random ID with 144 bits of entropy.
func NewID() (string, error) {
	buf := make([]byte, 18)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func ValidID(id string) bool {
	return safeIDPattern.MatchString(id)
}

func SafeFilename(name string) string {
	name = strings.TrimSpace(strings.ReplaceAll(name, "\\", "/"))
	if idx := strings.LastIndexByte(name, '/'); idx >= 0 {
		name = name[idx+1:]
	}
	if name == "" || name == "." || name == ".." {
		return "drop"
	}

	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '.', r == '-', r == '_', r == ' ':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}

	out := strings.TrimSpace(b.String())
	out = strings.Trim(out, ".")
	if out == "" {
		return "drop"
	}
	if len(out) > 160 {
		out = out[:160]
	}
	return out
}
