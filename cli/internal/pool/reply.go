package pool

import "strings"

// Reply finds the answer to a delivered message: the entries from the last
// user entry holding the message's opening words, once an assistant or error
// entry follows it. Agents wrap and reflow delivered text (a sender line,
// relay instructions, pane line breaks), so only the opening words are matched,
// ignoring whitespace.
func Reply(entries []Entry, sent string) ([]Entry, bool) {
	needle := []rune(squash(sent))
	if len(needle) > 60 {
		needle = needle[:60]
	}
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].Type != "user" || !strings.Contains(squash(entries[i].Text), string(needle)) {
			continue
		}
		for _, e := range entries[i+1:] {
			if e.Type == "assistant" || e.Type == "error" {
				return entries[i:], true
			}
		}
		return nil, false
	}
	return nil, false
}

func squash(s string) string { return strings.Join(strings.Fields(s), " ") }
