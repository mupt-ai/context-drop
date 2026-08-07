package drop

import (
	"strings"
	"testing"
)

func TestNewIDReturnsValidUniqueID(t *testing.T) {
	t.Parallel()

	first, err := NewID()
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewID()
	if err != nil {
		t.Fatal(err)
	}

	if !ValidID(first) {
		t.Fatalf("NewID() = %q, want valid ID", first)
	}
	if len(first) != 24 {
		t.Fatalf("len(NewID()) = %d, want 24", len(first))
	}
	if first == second {
		t.Fatalf("NewID returned duplicate IDs: %q", first)
	}
}

func TestValidID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		id   string
		want bool
	}{
		{name: "minimum length", id: "abcdefghijklmnop", want: true},
		{name: "maximum length", id: strings.Repeat("A", 64), want: true},
		{name: "url safe punctuation", id: "abcDEF0123_-xyz9", want: true},
		{name: "too short", id: "short", want: false},
		{name: "too long", id: strings.Repeat("A", 65), want: false},
		{name: "slash", id: "abcdefghijklmnop/", want: false},
		{name: "space", id: "abcdefghijklmno ", want: false},
		{name: "empty", id: "", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ValidID(tt.id); got != tt.want {
				t.Fatalf("ValidID(%q) = %v, want %v", tt.id, got, tt.want)
			}
		})
	}
}

func TestSafeFilename(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{name: "simple", in: "notes.txt", want: "notes.txt"},
		{name: "windows path", in: `C:\Users\me\shot.png`, want: "shot.png"},
		{name: "unix path", in: "/tmp/report.pdf", want: "report.pdf"},
		{name: "blank", in: "   ", want: "drop"},
		{name: "current directory", in: ".", want: "drop"},
		{name: "parent directory", in: "..", want: "drop"},
		{name: "unsafe characters", in: "hello:world🔥.txt", want: "hello_world_.txt"},
		{name: "trims dots", in: "...hidden...", want: "hidden"},
		{name: "only dots", in: "...", want: "drop"},
		{name: "keeps spaces", in: "  my file.txt  ", want: "my file.txt"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SafeFilename(tt.in); got != tt.want {
				t.Fatalf("SafeFilename(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestSafeFilenameTruncatesLongNames(t *testing.T) {
	t.Parallel()

	got := SafeFilename(strings.Repeat("a", 200))
	if len(got) != 160 {
		t.Fatalf("len(SafeFilename(long)) = %d, want 160", len(got))
	}
}
