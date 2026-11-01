package telegram

import (
	"strconv"
	"strings"
	"unicode/utf8"
)

// Outbound messages use parse_mode "HTML", not MarkdownV2. HTML needs three
// characters escaped; MarkdownV2 needs fourteen, and a single missed escape
// fails the whole send rather than degrading one line (spec §11.1).
const (
	// ParseMode is the value the client sends as parse_mode.
	ParseMode = "HTML"

	// TelegramMaxMessageChars is the hard Telegram limit for one message.
	TelegramMaxMessageChars = 4096
	// ChunkTarget is the size an assembled message may reach before it is
	// split. The gap to the hard limit absorbs the part header, the closing
	// tags a split has to re-emit, and the truncation notice.
	ChunkTarget = 4000
	// MaxParts is the most parts one response may occupy. Beyond this the
	// body is truncated and the reader is pointed at a fuller source.
	MaxParts = 3

	// MaxErrorChars bounds an error string interpolated into a message.
	MaxErrorChars = 500
	// MaxLogChars bounds a log excerpt interpolated into a message.
	MaxLogChars = 1000
)

// TruncationNotice closes a body that did not fit in MaxParts. The angle
// brackets are escaped: this string is appended to an HTML-parse-mode body, and
// a literal <id> would be an unsupported tag that fails the entire send.
const TruncationNotice = "… truncated. See /incident &lt;id&gt; or the dashboard."

// headerReserve is the room held back for the "(1/3)\n" part header, which is
// only known once the part count is.
const headerReserve = 8

// tagCloseReserve is the room held back for closing tags when a hard split
// lands in the middle of an over-long line.
const tagCloseReserve = 64

// maxTagDepth bounds the tag stack the chunker tracks. Nothing this package
// emits nests anywhere near this deep.
const maxTagDepth = 16

// htmlEscaper escapes the three characters Telegram's HTML parse mode treats as
// markup. It replaces in a single left-to-right pass, so an ampersand produced
// by an earlier replacement is never re-escaped — the same guarantee as
// escaping "&" first and "<" and ">" after it, without the ordering hazard.
var htmlEscaper = strings.NewReplacer(
	"&", "&amp;",
	"<", "&lt;",
	">", "&gt;",
)

// Esc escapes a value for interpolation into an HTML-parse-mode message.
// Every untrusted or free-form value must pass through it: commit SHAs, file
// paths, branch names and error strings all routinely contain "<", ">" and "&".
func Esc(s string) string { return htmlEscaper.Replace(s) }

// Bold wraps an escaped value in <b>.
func Bold(s string) string { return "<b>" + Esc(s) + "</b>" }

// Code wraps an escaped value in <code>.
func Code(s string) string { return "<code>" + Esc(s) + "</code>" }

// Pre wraps an escaped block in <pre>. Telegram renders <pre> in a monospace
// font, which is the only way a tabular section keeps its column alignment on a
// phone (spec §11.1).
func Pre(body string) string { return "<pre>" + Esc(body) + "</pre>" }

const (
	maxTableRows    = 64
	maxTableColumns = 6
	maxCellChars    = 48
)

// PreTable renders rows as a space-padded monospace block. Column widths are
// measured on the unescaped text, so escaping cannot skew the alignment. Rows,
// columns and cell width are all bounded; the input is operational data, but
// parts of it (service names, branch names) originate outside this service.
func PreTable(rows [][]string) string {
	if len(rows) == 0 {
		return ""
	}
	if len(rows) > maxTableRows {
		rows = rows[:maxTableRows]
	}

	cleaned := make([][]string, 0, len(rows))
	widths := make([]int, 0, maxTableColumns)
	for _, row := range rows {
		if len(row) > maxTableColumns {
			row = row[:maxTableColumns]
		}
		out := make([]string, len(row))
		for i, cell := range row {
			cell = strings.Map(dropControl, TruncateField(cell, maxCellChars))
			out[i] = cell
			w := utf8.RuneCountInString(cell)
			for len(widths) <= i {
				widths = append(widths, 0)
			}
			if w > widths[i] {
				widths[i] = w
			}
		}
		cleaned = append(cleaned, out)
	}

	var b strings.Builder
	b.WriteString("<pre>")
	for _, row := range cleaned {
		for i, cell := range row {
			b.WriteString(Esc(cell))
			// The final column of a row is never padded: trailing spaces
			// buy nothing and cost width on a narrow screen.
			if i < len(row)-1 {
				b.WriteString(strings.Repeat(" ", widths[i]-utf8.RuneCountInString(cell)+2))
			}
		}
		b.WriteString("\n")
	}
	b.WriteString("</pre>")
	return b.String()
}

func dropControl(r rune) rune {
	// Tabs and newlines inside a cell would destroy the alignment <pre> exists
	// to preserve.
	if r < 0x20 || r == 0x7f {
		return ' '
	}
	return r
}

// TruncateField bounds a value to max runes, marking the cut with an ellipsis.
//
// Truncation runs on the raw value, before Esc: cutting escaped text can slice
// an entity in half and turn "&amp;" into "&am".
func TruncateField(s string, max int) string {
	if max <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= max {
		return s
	}
	count := 0
	for i := range s {
		if count == max-1 {
			return s[:i] + "…"
		}
		count++
	}
	return s
}

// TruncateError bounds an error string before it is interpolated.
func TruncateError(s string) string { return TruncateField(s, MaxErrorChars) }

// TruncateLog bounds a log excerpt before it is interpolated.
func TruncateLog(s string) string { return TruncateField(s, MaxLogChars) }

// Chunk splits an assembled HTML body into sendable parts.
//
// Splitting happens on line boundaries, which is what makes the "never split an
// entity or a tag" guarantee cheap: nothing this package emits puts a newline
// inside "&amp;" or inside "<pre>". A single line longer than a part is split
// at a boundary that the scanner has proved is outside both.
//
// Each part is independently valid HTML. Telegram parses every message on its
// own, so a part that ends inside <pre> would fail to send; the chunker closes
// the open tags at the end of a part and re-opens them at the start of the next.
//
// At most MaxParts parts are produced. Anything past that is dropped and the
// last part ends with TruncationNotice — an ops channel is not the place to
// page through nine messages.
func Chunk(body string) []string {
	body = strings.ReplaceAll(body, "\r\n", "\n")
	body = strings.ReplaceAll(body, "\r", "\n")
	if strings.TrimSpace(body) == "" {
		return nil
	}
	if utf8.RuneCountInString(body) <= ChunkTarget {
		return []string{body}
	}

	lines := strings.Split(body, "\n")
	parts := make([]string, 0, MaxParts)
	var carry []openTag // tags left open by the previous part
	i := 0

	for i < len(lines) && len(parts) < MaxParts {
		budget := ChunkTarget - headerReserve
		if len(parts) == MaxParts-1 && remainingCost(lines, i, carry) > budget {
			// This is the last part we may emit and the rest does not fit:
			// hold back room for the notice now rather than overflow later.
			budget -= utf8.RuneCountInString(TruncationNotice) + 1
		}

		var b strings.Builder
		stack := append([]openTag(nil), carry...)
		used := 0
		for _, t := range stack {
			b.WriteString(t.raw)
			used += utf8.RuneCountInString(t.raw)
		}
		prefixUsed := used
		wrote := false

		for i < len(lines) {
			line := lines[i]
			after := applyTags(stack, line)
			cost := utf8.RuneCountInString(line) + 1 + closersLen(after)
			if used+cost <= budget {
				b.WriteString(line)
				b.WriteString("\n")
				used += utf8.RuneCountInString(line) + 1
				stack = after
				wrote = true
				i++
				continue
			}
			if wrote || used > prefixUsed {
				break // the line goes in the next part
			}
			// Nothing fits yet: this single line is longer than a whole part.
			avail := budget - used - 1 - tagCloseReserve
			head, tail := safeSplit(line, avail)
			if head == "" {
				// Unsplittable at any safe boundary; take it whole and accept
				// the overflow rather than emitting invalid HTML or looping.
				head, tail = line, ""
			}
			b.WriteString(head)
			b.WriteString("\n")
			used += utf8.RuneCountInString(head) + 1
			stack = applyTags(stack, head)
			wrote = true
			if tail == "" {
				i++
			} else {
				lines[i] = tail
			}
			break
		}

		content := strings.TrimRight(b.String(), "\n")
		content += closers(stack)
		parts = append(parts, content)
		carry = stack
	}

	if len(parts) == 0 {
		return nil
	}
	if i < len(lines) {
		last := len(parts) - 1
		parts[last] = parts[last] + "\n" + TruncationNotice
	}
	if len(parts) > 1 {
		total := len(parts)
		for n := range parts {
			parts[n] = "(" + strconv.Itoa(n+1) + "/" + strconv.Itoa(total) + ")\n" + parts[n]
		}
	}
	for n, p := range parts {
		if utf8.RuneCountInString(p) > TelegramMaxMessageChars {
			head, _ := safeSplit(p, TelegramMaxMessageChars-tagCloseReserve)
			if head != "" {
				parts[n] = head + closers(applyTags(nil, head))
			}
		}
	}
	return parts
}

// openTag is one unclosed tag, kept with its raw text so a split can re-open it
// with its attributes intact.
type openTag struct {
	name string
	raw  string
}

// applyTags walks a line and returns the tag stack as it stands after it.
// Content reaching the chunker has already passed through Esc, so every "<" in
// the body is a tag this package emitted.
func applyTags(stack []openTag, line string) []openTag {
	out := append([]openTag(nil), stack...)
	for i := 0; i < len(line); {
		if line[i] != '<' {
			i++
			continue
		}
		j := indexWithin(line[i:], '>', 128)
		if j < 0 {
			break // an unterminated "<": treat the rest as text
		}
		raw := line[i : i+j+1]
		i += j + 1
		inner := strings.TrimSpace(raw[1 : len(raw)-1])
		if inner == "" || strings.HasSuffix(inner, "/") {
			continue // "<>" or a self-closing tag such as <br/>
		}
		if inner[0] == '/' {
			name := strings.ToLower(strings.TrimSpace(inner[1:]))
			for k := len(out) - 1; k >= 0; k-- {
				if out[k].name == name {
					out = out[:k]
					break
				}
			}
			continue
		}
		name := inner
		if sp := strings.IndexAny(inner, " \t"); sp >= 0 {
			name = inner[:sp]
		}
		if len(out) >= maxTagDepth {
			continue
		}
		out = append(out, openTag{name: strings.ToLower(name), raw: raw})
	}
	return out
}

func closers(stack []openTag) string {
	if len(stack) == 0 {
		return ""
	}
	var b strings.Builder
	for k := len(stack) - 1; k >= 0; k-- {
		b.WriteString("</")
		b.WriteString(stack[k].name)
		b.WriteString(">")
	}
	return b.String()
}

func closersLen(stack []openTag) int {
	n := 0
	for _, t := range stack {
		n += len(t.name) + 3
	}
	return n
}

// remainingCost is a deliberate over-estimate of what is left to emit. It only
// decides whether to reserve room for the truncation notice, so erring high
// costs at most one early split and never an overflow.
func remainingCost(lines []string, from int, carry []openTag) int {
	n := closersLen(carry) * 2
	for _, t := range carry {
		n += utf8.RuneCountInString(t.raw)
	}
	for _, line := range lines[from:] {
		n += utf8.RuneCountInString(line) + 1
	}
	return n
}

// safeSplit cuts s at the last boundary within maxRunes that is neither inside
// a tag nor inside an HTML entity, preferring a word boundary when one is
// close. When no such boundary exists within the budget it takes the first one
// past it: a slightly over-long part is recoverable, a part carrying half an
// entity is not.
func safeSplit(s string, maxRunes int) (head, tail string) {
	if maxRunes <= 0 || s == "" {
		return "", s
	}
	best, bestSpace, first := -1, -1, -1
	i, runes := 0, 0
	for {
		if runes <= maxRunes {
			best = i
			if i > 0 && s[i-1] == ' ' {
				bestSpace = i
			}
		} else {
			first = i
			break
		}
		if i >= len(s) {
			break
		}
		switch s[i] {
		case '<':
			if j := indexWithin(s[i:], '>', 128); j >= 0 {
				runes += utf8.RuneCountInString(s[i : i+j+1])
				i += j + 1
				continue
			}
		case '&':
			if j := entityLen(s[i:]); j > 0 {
				runes += utf8.RuneCountInString(s[i : i+j])
				i += j
				continue
			}
		}
		_, size := utf8.DecodeRuneInString(s[i:])
		if size <= 0 {
			size = 1
		}
		i += size
		runes++
	}

	cut := best
	if bestSpace > 0 && best-bestSpace <= 64 {
		cut = bestSpace
	}
	if cut <= 0 {
		if first <= 0 {
			return "", s
		}
		cut = first
	}
	if cut >= len(s) {
		return s, ""
	}
	return strings.TrimRight(s[:cut], " "), strings.TrimLeft(s[cut:], " ")
}

// entityLen returns the byte length of the HTML entity starting at s, or 0 when
// s does not start one. An unterminated "&" is ordinary text and a legal place
// to split.
func entityLen(s string) int {
	const maxEntity = 12
	limit := len(s)
	if limit > maxEntity {
		limit = maxEntity
	}
	for j := 1; j < limit; j++ {
		c := s[j]
		if c == ';' {
			return j + 1
		}
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '#') {
			return 0
		}
	}
	return 0
}

func indexWithin(s string, c byte, limit int) int {
	if len(s) < limit {
		limit = len(s)
	}
	for i := 0; i < limit; i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}
