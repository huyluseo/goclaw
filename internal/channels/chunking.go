package channels

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// ChunkMarkdown splits markdown text into chunks of at most maxLen bytes,
// respecting fenced code blocks (``` ... ```). Prefers paragraph > line > space
// boundaries. Force-splits oversized code blocks with fence repair (close/reopen).
func ChunkMarkdown(text string, maxLen int) []string {
	return chunkMarkdown(text, maxLen, findSafeSplit)
}

// ChunkMarkdownBySentence is ChunkMarkdown for flowing prose: it also prefers
// sentence ends over bare spaces, and skips boundaries in the first half of a
// chunk when a later one exists, so chunks stay full and end cleanly.
func ChunkMarkdownBySentence(text string, maxLen int) []string {
	return chunkMarkdown(text, maxLen, findReadableSplit)
}

func chunkMarkdown(text string, maxLen int, findSplit func(window string) int) []string {
	if text == "" || maxLen <= 0 {
		return nil
	}
	if len(text) <= maxLen {
		return []string{text}
	}

	var chunks []string
	remaining := text
	needFenceReopen := false

	for len(remaining) > 0 {
		// Repair fence from previous force-split
		if needFenceReopen {
			remaining = "```\n" + remaining
			needFenceReopen = false
		}

		if len(remaining) <= maxLen {
			chunks = append(chunks, remaining)
			break
		}

		window := remaining[:maxLen]
		cutAt := findSplit(window)

		if cutAt > 0 {
			// Safe split found outside fenced block
			chunks = append(chunks, strings.TrimRight(remaining[:cutAt], " \n"))
			remaining = strings.TrimLeft(remaining[cutAt:], "\n")
		} else {
			// No safe split — force at maxLen, adjust to rune boundary
			cutAt := maxLen
			for cutAt > 0 && !utf8.RuneStart(remaining[cutAt]) {
				cutAt--
			}
			if cutAt == 0 {
				cutAt = maxLen // shouldn't happen, but prevent infinite loop
			}

			if isInFence(window) {
				chunks = append(chunks, remaining[:cutAt]+"\n```")
				needFenceReopen = true
			} else {
				chunks = append(chunks, strings.TrimRight(remaining[:cutAt], " \n"))
			}
			remaining = remaining[cutAt:]
		}
	}

	return chunks
}

// findSafeSplit finds the best split position in window that is NOT inside
// a fenced code block. Returns -1 if no safe point exists.
// Preference: paragraph (\n\n) > line (\n) > space.
func findSafeSplit(window string) int {
	points := scanSplitPoints(window)
	if points.para > 0 {
		return points.para
	}
	if points.line > 0 {
		return points.line
	}
	return points.space
}

// findReadableSplit prefers paragraph > line > sentence > space within the
// second half of window, then the same order anywhere. Returns -1 if no safe
// point exists.
func findReadableSplit(window string) int {
	points := scanSplitPoints(window)
	candidates := []int{points.para, points.line, points.sentence, points.space}
	for _, minPos := range []int{len(window) / 2, 1} {
		for _, pos := range candidates {
			if pos >= minPos {
				return pos
			}
		}
	}
	return -1
}

// splitPoints holds the last position of each boundary kind, -1 when absent.
type splitPoints struct {
	para, line, sentence, space int
}

// scanSplitPoints records boundaries that are NOT inside a fenced code block.
func scanSplitPoints(window string) splitPoints {
	inFence := false
	points := splitPoints{para: -1, line: -1, sentence: -1, space: -1}

	for i := 0; i < len(window); i++ {
		// Detect fence lines (3+ backticks at start of line)
		if i == 0 || (i > 0 && window[i-1] == '\n') {
			if hasFencePrefix(window[i:]) {
				inFence = !inFence
			}
		}

		if inFence {
			continue
		}

		switch window[i] {
		case '\n':
			if i+1 < len(window) && window[i+1] == '\n' {
				points.para = i + 2
			} else {
				points.line = i + 1
			}
		case ' ':
			points.space = i + 1
			if endsSentence(window[:i]) {
				points.sentence = i + 1
			}
		}
	}

	return points
}

// endsSentence reports whether s ends in sentence punctuation, looking past
// closing quotes, brackets and emphasis. A period after a digit does not count
// because it usually marks a numbered item ("1. ").
func endsSentence(s string) bool {
	s = strings.TrimRight(s, "\"')]*_”’")
	r, size := utf8.DecodeLastRuneInString(s)
	switch r {
	case '!', '?', '…':
		return true
	case '.':
		prev, _ := utf8.DecodeLastRuneInString(s[:len(s)-size])
		return !unicode.IsDigit(prev)
	}
	return false
}

// isInFence returns true if the end of window is inside an unclosed fenced code block.
func isInFence(window string) bool {
	inFence := false
	for i := 0; i < len(window); i++ {
		if i == 0 || (i > 0 && window[i-1] == '\n') {
			if hasFencePrefix(window[i:]) {
				inFence = !inFence
			}
		}
	}
	return inFence
}

// hasFencePrefix returns true if s starts with 3+ backticks (fenced code block marker).
func hasFencePrefix(s string) bool {
	return len(s) >= 3 && s[0] == '`' && s[1] == '`' && s[2] == '`'
}
