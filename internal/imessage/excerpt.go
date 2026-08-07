package imessage

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"regexp"
	"sort"
	"strings"
)

const (
	// BeginningTranscriptBudget is the verbatim head of the chat always
	// included in the prompt so "first message" / long-run questions are
	// answerable even when the session's recent window gets compacted.
	BeginningTranscriptBudget = 64 * 1024
	// RetrievalTranscriptBudget is the total size of older excerpts pulled
	// from the corpus by lexical relevance to the incoming message.
	RetrievalTranscriptBudget = 96 * 1024
)

type chatLine struct {
	TS   string `json:"ts"`
	Role string `json:"role"`
	Text string `json:"text"`
}

var wordRe = regexp.MustCompile(`[^a-z0-9]+`)

func chatTokens(s string) map[string]int {
	counts := map[string]int{}
	for _, w := range wordRe.Split(strings.ToLower(s), -1) {
		if len(w) >= 3 {
			counts[w]++
		}
	}
	return counts
}

func formatChatLine(e chatLine) string {
	who := "Assistant"
	if e.Role == "user" {
		who = "Avyay"
	}
	return "[" + e.TS + "] " + who + ": " + e.Text
}

// conversationExcerpts returns the verbatim head of the chat plus the
// excerpts most relevant to query, formatted for inclusion in the prompt.
func conversationExcerpts(archivePath, query string) (string, error) {
	if archivePath == "" {
		return "", nil
	}
	f, err := os.Open(archivePath)
	if err != nil {
		return "", fmt.Errorf("open conversation archive: %w", err)
	}
	defer f.Close()

	var entries []chatLine
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e chatLine
		if err := json.Unmarshal([]byte(line), &e); err != nil || strings.TrimSpace(e.Text) == "" {
			continue
		}
		entries = append(entries, e)
	}
	if err := sc.Err(); err != nil {
		return "", fmt.Errorf("read conversation archive: %w", err)
	}
	if len(entries) == 0 {
		return "", nil
	}

	used := make([]bool, len(entries))
	var head strings.Builder
	for i, e := range entries {
		formatted := formatChatLine(e)
		if head.Len()+len(formatted)+1 > BeginningTranscriptBudget {
			break
		}
		head.WriteString(formatted + "\n")
		used[i] = true
	}

	queryTokens := chatTokens(query)
	var retrieved strings.Builder
	if len(queryTokens) > 0 {
		df := map[string]int{}
		for _, e := range entries {
			for w := range chatTokens(e.Text) {
				df[w] = df[w] + 1
			}
		}
		n := len(entries)
		type scored struct {
			index int
			score float64
			ts    string
		}
		var scoredList []scored
		for i, e := range entries {
			if used[i] {
				continue
			}
			docTokens := chatTokens(e.Text)
			var score float64
			for w, qc := range queryTokens {
				c, ok := docTokens[w]
				if !ok {
					continue
				}
				dfw := df[w]
				if dfw == 0 {
					continue
				}
				idf := math.Log(1 + float64(n)/float64(dfw))
				score += float64(qc) * float64(c) * idf
			}
			if score > 0 {
				scoredList = append(scoredList, scored{i, score, e.TS})
			}
		}
		sort.SliceStable(scoredList, func(a, b int) bool {
			if scoredList[a].score != scoredList[b].score {
				return scoredList[a].score > scoredList[b].score
			}
			return scoredList[a].ts < scoredList[b].ts
		})
		for _, s := range scoredList {
			if retrieved.Len() >= RetrievalTranscriptBudget {
				break
			}
			entriesToShow := []int{s.index}
			if s.index+1 < len(entries) {
				entriesToShow = append(entriesToShow, s.index+1)
			}
			for _, idx := range entriesToShow {
				if used[idx] {
					continue
				}
				formatted := formatChatLine(entries[idx])
				if retrieved.Len()+len(formatted)+1 > RetrievalTranscriptBudget {
					continue
				}
				retrieved.WriteString(formatted + "\n")
				used[idx] = true
			}
		}
	}

	var out strings.Builder
	out.WriteString("Ground truth: the first recorded message in this chat is " + formatChatLine(entries[0]) + ". Do not infer the beginning from recent session messages.\n\n")
	out.WriteString(head.String())
	if retrieved.Len() > 0 {
		out.WriteString("\nMost relevant older exchanges:\n\n")
		out.WriteString(retrieved.String())
	}
	return out.String(), nil
}
