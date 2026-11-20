package cli

import (
	"reflect"
	"testing"
)

func TestParseKeysDecodesArrowsAndText(t *testing.T) {
	cases := []struct {
		in   []byte
		want []keyPress
	}{
		{[]byte("\x1b[A"), []keyPress{{name: "up"}}},
		{[]byte("\x1b[B\x1b[C\x1b[D"), []keyPress{{name: "down"}, {name: "right"}, {name: "left"}}},
		{[]byte("\r"), []keyPress{{name: "enter"}}},
		{[]byte{127}, []keyPress{{name: "backspace"}}},
		{[]byte{3}, []keyPress{{name: "ctrl-c"}}},
		{[]byte("\x1b"), []keyPress{{name: "esc"}}},
		{[]byte("sk-1"), []keyPress{{name: "text", text: "sk-1"}}},
		{[]byte("ab\r"), []keyPress{{name: "text", text: "ab"}, {name: "enter"}}},
		{[]byte("é"), []keyPress{{name: "text", text: "é"}}},
	}
	for _, c := range cases {
		if got := parseKeys(c.in); !reflect.DeepEqual(got, c.want) {
			t.Errorf("parseKeys(%q) = %+v, want %+v", c.in, got, c.want)
		}
	}
}
