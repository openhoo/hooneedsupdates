package update

import (
	"bytes"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

type diffLine struct {
	kind byte
	text string
}

// WriteDiff emits contextual text hunks and hash summaries for binary files.
// A bounded line comparison keeps review predictable for large lockfiles.
func WriteDiff(output io.Writer, files []AppliedFile) error {
	for _, file := range files {
		if bytes.Equal(file.Before, file.After) {
			continue
		}
		if !utf8.Valid(file.Before) || !utf8.Valid(file.After) || bytes.IndexByte(file.Before, 0) >= 0 || bytes.IndexByte(file.After, 0) >= 0 {
			if _, err := fmt.Fprintf(output, "Binary %s: %s -> %s\n", quoteDiffPath(file.Path), contentDigest(file.Before), contentDigest(file.After)); err != nil {
				return err
			}
			continue
		}
		lines := lineDiff(diffLines(file.Before), diffLines(file.After))
		oldName := "a/" + file.Path
		if file.Created {
			oldName = "/dev/null"
		}
		if _, err := fmt.Fprintf(output, "--- %s\n+++ %s\n", quoteDiffPath(oldName), quoteDiffPath("b/"+file.Path)); err != nil {
			return err
		}
		var changed []int
		for i, line := range lines {
			if line.kind != ' ' {
				changed = append(changed, i)
			}
		}
		oldConsumed, newConsumed, cursor := 0, 0, 0
		for i := 0; i < len(changed); {
			first, last := changed[i], changed[i]
			i++
			for i < len(changed) && changed[i] <= last+7 {
				last = changed[i]
				i++
			}
			start, end := max(0, first-3), min(len(lines), last+4)
			for cursor < start {
				line := lines[cursor]
				if line.kind != '+' {
					oldConsumed++
				}
				if line.kind != '-' {
					newConsumed++
				}
				cursor++
			}
			oldCount, newCount := 0, 0
			for _, line := range lines[start:end] {
				if line.kind != '+' {
					oldCount++
				}
				if line.kind != '-' {
					newCount++
				}
			}
			oldStart, newStart := oldConsumed, newConsumed
			if oldCount > 0 {
				oldStart++
			}
			if newCount > 0 {
				newStart++
			}
			if _, err := fmt.Fprintf(output, "@@ -%d,%d +%d,%d @@\n", oldStart, oldCount, newStart, newCount); err != nil {
				return err
			}
			for cursor < end {
				line := lines[cursor]
				if _, err := fmt.Fprintf(output, "%c%s", line.kind, line.text); err != nil {
					return err
				}
				if !strings.HasSuffix(line.text, "\n") {
					if _, err := io.WriteString(output, "\n\\ No newline at end of file\n"); err != nil {
						return err
					}
				}
				if line.kind != '+' {
					oldConsumed++
				}
				if line.kind != '-' {
					newConsumed++
				}
				cursor++
			}
		}
	}
	return nil
}

func lineDiff(old, next []string) []diffLine {
	prefix, suffix := 0, 0
	for prefix < len(old) && prefix < len(next) && old[prefix] == next[prefix] {
		prefix++
	}
	for suffix < len(old)-prefix && suffix < len(next)-prefix && old[len(old)-1-suffix] == next[len(next)-1-suffix] {
		suffix++
	}
	result := make([]diffLine, 0, len(old)+len(next))
	for _, line := range old[:prefix] {
		result = append(result, diffLine{' ', line})
	}
	a, b := old[prefix:len(old)-suffix], next[prefix:len(next)-suffix]
	// Limit the dynamic-programming table to one million interior cells. The
	// fallback remains exact and bounded, with a broader replacement hunk.
	if len(a) == 0 || len(b) == 0 || len(a) > 1000000/len(b) {
		for _, line := range a {
			result = append(result, diffLine{'-', line})
		}
		for _, line := range b {
			result = append(result, diffLine{'+', line})
		}
	} else {
		width := len(b) + 1
		table := make([]uint32, (len(a)+1)*width)
		for i := len(a) - 1; i >= 0; i-- {
			for j := len(b) - 1; j >= 0; j-- {
				if a[i] == b[j] {
					table[i*width+j] = 1 + table[(i+1)*width+j+1]
				} else {
					table[i*width+j] = max(table[(i+1)*width+j], table[i*width+j+1])
				}
			}
		}
		i, j := 0, 0
		for i < len(a) || j < len(b) {
			if i < len(a) && j < len(b) && a[i] == b[j] {
				result = append(result, diffLine{' ', a[i]})
				i++
				j++
			} else if i < len(a) && (j == len(b) || table[(i+1)*width+j] >= table[i*width+j+1]) {
				result = append(result, diffLine{'-', a[i]})
				i++
			} else {
				result = append(result, diffLine{'+', b[j]})
				j++
			}
		}
	}
	for _, line := range old[len(old)-suffix:] {
		result = append(result, diffLine{' ', line})
	}
	return result
}

func quoteDiffPath(path string) string {
	safe := true
	for _, b := range []byte(path) {
		if b <= 32 || b >= 127 || b == '"' || b == '\\' {
			safe = false
			break
		}
	}
	if safe {
		return path
	}
	var quoted strings.Builder
	quoted.WriteByte('"')
	for _, b := range []byte(path) {
		switch b {
		case '"', '\\':
			quoted.WriteByte('\\')
			quoted.WriteByte(b)
		case '\n':
			quoted.WriteString(`\n`)
		case '\r':
			quoted.WriteString(`\r`)
		case '\t':
			quoted.WriteString(`\t`)
		default:
			if b < 32 || b >= 127 {
				fmt.Fprintf(&quoted, `\%03o`, b)
			} else {
				quoted.WriteByte(b)
			}
		}
	}
	quoted.WriteByte('"')
	return quoted.String()
}

func diffLines(data []byte) []string {
	if len(data) == 0 {
		return nil
	}
	lines := strings.SplitAfter(string(data), "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	return lines
}
