package cli

import (
	"reflect"
	"testing"

	"contextdrop.dev/context-drop/internal/config"
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

func TestDefaultChoicePrefersConfiguredThenKeyedThenOllama(t *testing.T) {
	choices := []modelChoice{
		{label: "a", sc: configSummarize("anthropic", "claude-haiku-5-5"), keyEnvs: []string{"NOPE_KEY"}},
		{label: "b", sc: configSummarize("ollama", "qwen"), ask: ""},
	}
	oldGet := getenv
	t.Cleanup(func() { getenv = oldGet })
	getenv = func(string) string { return "" }
	if got := defaultChoice(choices, configCurrent("", "")); got != 1 {
		t.Errorf("no keys: default = %d, want the Ollama model", got)
	}
	getenv = func(k string) string { return map[string]string{"NOPE_KEY": "x"}[k] }
	if got := defaultChoice(choices, configCurrent("", "")); got != 0 {
		t.Errorf("key set: default = %d, want the keyed cloud model", got)
	}
	if got := defaultChoice(choices, configCurrent("ollama", "qwen")); got != 1 {
		t.Errorf("configured model: default = %d", got)
	}
}

func configSummarize(provider, model string) config.SummarizeConfig {
	return config.SummarizeConfig{Provider: provider, Model: model}
}

func configCurrent(provider, model string) config.SummarizeConfig {
	return configSummarize(provider, model)
}
