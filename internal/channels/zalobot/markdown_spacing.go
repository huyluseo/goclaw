package zalobot

import (
	"regexp"
	"strings"
)

var (
	listItemPattern = regexp.MustCompile(`^\s*(?:[-*+]|\d+[.)])\s`)
	// Markdown lets only bullets and lists starting at 1 follow a paragraph
	// without a blank line; any other number would merge into the paragraph.
	listStartPattern = regexp.MustCompile(`^\s*(?:[-*+]|1[.)])\s`)
)

// compactListSpacing removes blank lines before list items, because Zalo draws
// them on top of the margin it already gives a list. Blank lines after a list
// stay: without them the next paragraph would become part of the last item.
func compactListSpacing(text string) string {
	lines := strings.Split(text, "\n")
	kept := make([]string, 0, len(lines))
	inFence := false

	for i, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			inFence = !inFence
		}
		if !inFence && strings.TrimSpace(line) == "" && precedesListItem(lines, kept, i) {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

// precedesListItem reports whether the blank line at index i sits between
// already kept text and a list item that can directly follow that text.
func precedesListItem(lines, kept []string, i int) bool {
	if len(kept) == 0 {
		return false
	}
	previous := kept[len(kept)-1]
	if strings.TrimSpace(previous) == "" {
		return false
	}
	for _, next := range lines[i+1:] {
		if strings.TrimSpace(next) == "" {
			continue
		}
		return listStartPattern.MatchString(next) ||
			(listItemPattern.MatchString(next) && listItemPattern.MatchString(previous))
	}
	return false
}
