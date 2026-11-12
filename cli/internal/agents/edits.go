package agents

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"time"
)

// Edit is a file an agent's transcript shows it writing.
type Edit struct {
	Path string    `json:"path"`
	Time time.Time `json:"time"`
}

// editBytes bounds how much of a transcript is scanned for edits.
const editBytes = 2 << 20

// EditedFiles lists the files a session's transcript shows it editing since
// the given time: Claude Code's Edit, MultiEdit, Write, and NotebookEdit
// tools, Codex's apply_patch, and Pi's edit and write tools. Other agents
// (OpenCode) report nothing. Relative paths are taken from the session's
// working directory.
func EditedFiles(s Session, since time.Time) []Edit {
	if s.Transcript == "" {
		return nil
	}
	rows, err := readJSONLTail(s.Transcript, editBytes)
	if err != nil {
		return nil
	}
	var edits []Edit
	add := func(path string, t time.Time) {
		if path == "" || (!since.IsZero() && !t.IsZero() && t.Before(since)) {
			return
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(s.Cwd, path)
		}
		edits = append(edits, Edit{Path: filepath.Clean(path), Time: t})
	}
	for _, row := range rows {
		t := parseTime(str(row, "timestamp"))
		switch s.Agent {
		case Claude:
			if str(row, "type") != "assistant" {
				continue
			}
			for _, b := range blocks(obj(row, "message")["content"]) {
				switch str(b, "name") {
				case "Edit", "MultiEdit", "Write":
					add(str(b, "input", "file_path"), t)
				case "NotebookEdit":
					add(str(b, "input", "notebook_path"), t)
				}
			}
		case Codex:
			payload := obj(row, "payload")
			if str(row, "type") != "response_item" || str(payload, "name") != "apply_patch" {
				continue
			}
			patch := str(payload, "input")
			if patch == "" { // function-call form: {"input": "*** Begin Patch…"}
				var args struct{ Input string }
				_ = json.Unmarshal([]byte(str(payload, "arguments")), &args)
				patch = args.Input
			}
			for _, path := range patchFiles(patch) {
				add(path, t)
			}
		case Pi:
			msg := obj(row, "message")
			if str(row, "type") != "message" || str(msg, "role") != "assistant" {
				continue
			}
			for _, b := range blocks(msg["content"]) {
				if name := strings.ToLower(str(b, "name")); str(b, "type") == "toolCall" && (name == "edit" || name == "write") {
					add(firstNonEmpty(str(b, "arguments", "path"), str(b, "arguments", "file_path")), t)
				}
			}
		}
	}
	return edits
}

func blocks(content any) []map[string]any {
	list, _ := content.([]any)
	var out []map[string]any
	for _, item := range list {
		if b, ok := item.(map[string]any); ok {
			out = append(out, b)
		}
	}
	return out
}

// patchFiles lists the files an apply_patch patch adds, updates, deletes, or moves to.
func patchFiles(patch string) []string {
	var files []string
	for _, line := range strings.Split(patch, "\n") {
		for _, prefix := range []string{"*** Update File:", "*** Add File:", "*** Delete File:", "*** Move to:"} {
			if rest, ok := strings.CutPrefix(line, prefix); ok {
				files = append(files, strings.TrimSpace(rest))
			}
		}
	}
	return files
}
