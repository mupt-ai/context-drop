package agents

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// tailBytes bounds how much of a transcript is parsed for reading and status.
const tailBytes = 8 << 20

// readJSONLTail decodes the JSON objects in the last max bytes of a JSONL file.
// A partial first line is dropped.
func readJSONLTail(path string, max int64) ([]map[string]any, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	start := int64(0)
	if info.Size() > max {
		start = info.Size() - max
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return nil, err
	}
	reader := bufio.NewReaderSize(f, 1<<20)
	if start > 0 {
		if _, err := reader.ReadString('\n'); err != nil {
			return nil, nil
		}
	}
	var rows []map[string]any
	for {
		line, err := reader.ReadBytes('\n')
		if len(strings.TrimSpace(string(line))) > 0 {
			var row map[string]any
			if json.Unmarshal(line, &row) == nil {
				rows = append(rows, row)
			}
		}
		if err != nil {
			break
		}
	}
	return rows, nil
}

// readFirstJSONLine decodes the first line of a JSONL file.
func readFirstJSONLine(path string) (map[string]any, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	reader := bufio.NewReaderSize(f, 1<<20)
	line, err := reader.ReadBytes('\n')
	if err != nil && len(line) == 0 {
		return nil, err
	}
	var row map[string]any
	if err := json.Unmarshal(line, &row); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return row, nil
}

func modTime(path string) time.Time {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return info.ModTime()
}

// JSON accessors for loosely typed transcript rows.

func str(m map[string]any, keys ...string) string {
	var cur any = m
	for _, k := range keys {
		obj, ok := cur.(map[string]any)
		if !ok {
			return ""
		}
		cur = obj[k]
	}
	s, _ := cur.(string)
	return s
}

func obj(m map[string]any, keys ...string) map[string]any {
	var cur any = m
	for _, k := range keys {
		o, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = o[k]
	}
	o, _ := cur.(map[string]any)
	return o
}

func num(m map[string]any, key string) float64 {
	v, _ := m[key].(float64)
	return v
}

func parseTime(v string) time.Time {
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, v); err == nil {
			return t
		}
	}
	return time.Time{}
}

func millis(v float64) time.Time {
	if v <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(int64(v))
}

func micros(v float64) time.Time {
	if v <= 0 {
		return time.Time{}
	}
	return time.UnixMicro(int64(v))
}

// blockText joins the text blocks of a content value that is either a string
// or a list of {type, text} blocks. Tool calls are summarized in one line.
func blockText(content any, textTypes ...string) string {
	switch c := content.(type) {
	case string:
		return c
	case []any:
		allowed := map[string]bool{}
		for _, t := range textTypes {
			allowed[t] = true
		}
		var parts []string
		for _, item := range c {
			b, ok := item.(map[string]any)
			if !ok {
				continue
			}
			typ, _ := b["type"].(string)
			switch {
			case allowed[typ]:
				if t, _ := b["text"].(string); t != "" {
					parts = append(parts, t)
				}
			case typ == "tool_use" || typ == "toolCall":
				name, _ := b["name"].(string)
				parts = append(parts, "[tool: "+name+"]")
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}

func appendMessage(msgs []Message, role, text string, t time.Time) []Message {
	text = strings.TrimSpace(text)
	if text == "" {
		return msgs
	}
	return append(msgs, Message{Role: role, Text: text, Time: t})
}
