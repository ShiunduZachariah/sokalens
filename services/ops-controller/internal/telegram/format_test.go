package telegram

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestEscEscapesTheThreeHTMLCharacters(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "all clear", "all clear"},
		{"angle brackets", "<b>not markup</b>", "&lt;b&gt;not markup&lt;/b&gt;"},
		{"ampersand", "fixtures & odds", "fixtures &amp; odds"},
		{
			// Commit subjects routinely carry all three.
			name: "commit subject",
			in:   `fix(api): guard a<b && c>d in <Predictor/>`,
			want: `fix(api): guard a&lt;b &amp;&amp; c&gt;d in &lt;Predictor/&gt;`,
		},
		{
			// Generated diff paths and template names look like tags.
			name: "file path",
			in:   "services/<svc>/internal/a&b/main.go",
			want: "services/&lt;svc&gt;/internal/a&amp;b/main.go",
		},
		{"short sha with angle", "abc123f <HEAD>", "abc123f &lt;HEAD&gt;"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Esc(tc.in); got != tc.want {
				t.Fatalf("Esc(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestEscDoesNotDoubleEscapeTheAmpersand(t *testing.T) {
	// The ampersand must be escaped before "<" and ">", never after: escaping
	// it afterwards turns "&lt;" into "&amp;lt;lt;"-style garbage. The input
	// here already looks like an entity, which is exactly the case that
	// catches an out-of-order implementation.
	cases := map[string]string{
		"&lt;":   "&amp;lt;",
		"&amp;":  "&amp;amp;",
		"a<b&c":  "a&lt;b&amp;c",
		"&<>":    "&amp;&lt;&gt;",
		"&&&":    "&amp;&amp;&amp;",
		"&#x27;": "&amp;#x27;",
	}
	for in, want := range cases {
		if got := Esc(in); got != want {
			t.Fatalf("Esc(%q) = %q, want %q", in, got, want)
		}
	}
	// Escaping is not idempotent, and must not be: escaping twice is a bug at
	// the call site, and this pins the behaviour so it is visible.
	if got := Esc(Esc("&")); got != "&amp;amp;" {
		t.Fatalf("Esc(Esc(\"&\")) = %q", got)
	}
}

func TestPreEscapesItsContents(t *testing.T) {
	got := Pre("web  <down>\napi  ok & up")
	want := "<pre>web  &lt;down&gt;\napi  ok &amp; up</pre>"
	if got != want {
		t.Fatalf("Pre = %q, want %q", got, want)
	}
}

func TestPreTableAlignsOnUnescapedWidth(t *testing.T) {
	got := PreTable([][]string{
		{"web", "ok"},
		{"a&b", "degraded"},
		{"x", "ok"},
	})
	if !strings.HasPrefix(got, "<pre>") || !strings.HasSuffix(got, "</pre>") {
		t.Fatalf("PreTable is not a <pre> block: %q", got)
	}
	// "a&b" is three characters wide on screen even though it escapes to
	// seven, so the second column must start at the same offset on every row.
	lines := strings.Split(strings.TrimSuffix(strings.TrimPrefix(got, "<pre>"), "</pre>"), "\n")
	var offsets []int
	for _, line := range lines {
		if line == "" {
			continue
		}
		plain := strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">").Replace(line)
		offsets = append(offsets, strings.Index(plain, strings.TrimLeft(plain[3:], " ")))
	}
	for i := 1; i < len(offsets); i++ {
		if offsets[i] != offsets[0] {
			t.Fatalf("column offsets differ: %v\n%s", offsets, got)
		}
	}
	if PreTable(nil) != "" {
		t.Fatal("an empty table rendered a block")
	}
}

func TestTruncateFieldRunsOnRawTextAndKeepsRunesWhole(t *testing.T) {
	if got := TruncateField("short", 100); got != "short" {
		t.Fatalf("TruncateField = %q", got)
	}
	long := strings.Repeat("é", 40)
	got := TruncateField(long, 10)
	if n := utf8.RuneCountInString(got); n != 10 {
		t.Fatalf("rune count = %d, want 10", n)
	}
	if !utf8.ValidString(got) {
		t.Fatal("truncation split a multi-byte rune")
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatalf("truncation is unmarked: %q", got)
	}

	if n := utf8.RuneCountInString(TruncateError(strings.Repeat("e", 5000))); n != MaxErrorChars {
		t.Fatalf("TruncateError length = %d, want %d", n, MaxErrorChars)
	}
	if n := utf8.RuneCountInString(TruncateLog(strings.Repeat("l", 5000))); n != MaxLogChars {
		t.Fatalf("TruncateLog length = %d, want %d", n, MaxLogChars)
	}

	// Truncating before escaping is the whole point: escaping first and
	// cutting afterwards can leave half an entity behind.
	escaped := Esc(TruncateField(strings.Repeat("&", 5000), MaxErrorChars))
	assertNoSplitEntities(t, escaped)
}

func TestChunkKeepsShortBodiesWhole(t *testing.T) {
	if got := Chunk(""); got != nil {
		t.Fatalf("Chunk(\"\") = %q, want nil", got)
	}
	if got := Chunk("   \n  "); got != nil {
		t.Fatalf("Chunk(whitespace) = %q, want nil", got)
	}
	body := "<b>Status</b>\n" + Pre("web  ok\napi  ok")
	parts := Chunk(body)
	if len(parts) != 1 || parts[0] != body {
		t.Fatalf("a short body was altered: %q", parts)
	}
	// A single part carries no "(1/1)" header — numbering a lone message is
	// noise.
	if strings.HasPrefix(parts[0], "(") {
		t.Fatalf("a single part was numbered: %q", parts[0])
	}
}

func TestChunkBoundaryAt4095And4096And4097(t *testing.T) {
	for _, size := range []int{ChunkTarget - 1, ChunkTarget, ChunkTarget + 1, 4095, 4096, 4097} {
		body := linesOfSize(size)
		if got := utf8.RuneCountInString(body); got != size {
			t.Fatalf("test body is %d characters, want %d", got, size)
		}
		parts := Chunk(body)
		if len(parts) == 0 {
			t.Fatalf("size %d produced no parts", size)
		}
		wantSingle := size <= ChunkTarget
		if wantSingle != (len(parts) == 1) {
			t.Fatalf("size %d produced %d parts (single expected: %v)", size, len(parts), wantSingle)
		}
		for i, p := range parts {
			if n := utf8.RuneCountInString(p); n > TelegramMaxMessageChars {
				t.Fatalf("size %d part %d is %d characters, over the Telegram limit", size, i+1, n)
			}
			assertValidHTMLPart(t, p)
		}
		if len(parts) > 1 {
			assertNumbered(t, parts)
		}
		if size <= ChunkTarget && parts[0] != body {
			t.Fatalf("size %d was altered although it fits", size)
		}
	}
}

func TestChunkTruncatesAFourPartBodyToThree(t *testing.T) {
	body := linesOfSize(4 * ChunkTarget)
	parts := Chunk(body)

	if len(parts) != MaxParts {
		t.Fatalf("got %d parts, want %d", len(parts), MaxParts)
	}
	last := parts[len(parts)-1]
	if !strings.HasSuffix(last, TruncationNotice) {
		t.Fatalf("the last part does not carry the truncation notice:\n%s", last[max(0, len(last)-120):])
	}
	// The notice points somewhere the rest of the body can actually be read.
	if !strings.Contains(last, "/incident") || !strings.Contains(last, "dashboard") {
		t.Fatalf("the truncation notice lost its pointer: %q", last)
	}
	// It must be escaped: a literal <id> is an unsupported tag and Telegram
	// rejects the whole message.
	if strings.Contains(last, "<id>") {
		t.Fatalf("the truncation notice contains an unescaped tag: %q", last)
	}
	for i, p := range parts {
		if n := utf8.RuneCountInString(p); n > TelegramMaxMessageChars {
			t.Fatalf("part %d is %d characters", i+1, n)
		}
		assertValidHTMLPart(t, p)
	}
	assertNumbered(t, parts)
}

func TestChunkNeverSplitsAnEntityOrATag(t *testing.T) {
	// A long body built entirely from entities and tags: any naive cut lands
	// inside one of them.
	var b strings.Builder
	for i := 0; i < 400; i++ {
		b.WriteString("<b>")
		b.WriteString(Esc(`svc<a&b> "quoted" & more & more`))
		b.WriteString("</b>\n")
	}
	for _, p := range Chunk(b.String()) {
		assertValidHTMLPart(t, p)
	}

	// The same, with no line breaks at all, so the chunker has to hard-split
	// a single over-long line.
	single := strings.Repeat(Esc(`a<b>&c `), 900)
	parts := Chunk(single)
	if len(parts) < 2 {
		t.Fatalf("a %d-character single line was not split", utf8.RuneCountInString(single))
	}
	for i, p := range parts {
		if n := utf8.RuneCountInString(p); n > TelegramMaxMessageChars {
			t.Fatalf("part %d is %d characters", i+1, n)
		}
		assertValidHTMLPart(t, p)
	}
}

func TestChunkReopensTagsSoEveryPartStandsAlone(t *testing.T) {
	// A <pre> block bigger than one part. Telegram parses each message on its
	// own, so part 1 has to close the block and part 2 has to reopen it.
	var b strings.Builder
	b.WriteString("<b>Queues</b>\n<pre>")
	for i := 0; i < 700; i++ {
		b.WriteString("predictions  pending=12  oldest=4m\n")
	}
	b.WriteString("</pre>")

	parts := Chunk(b.String())
	if len(parts) < 2 {
		t.Fatal("the oversized <pre> block was not split")
	}
	for i, p := range parts {
		assertValidHTMLPart(t, p)
		if !strings.Contains(p, "<pre>") || !strings.Contains(p, "</pre>") {
			t.Fatalf("part %d does not carry a self-contained <pre> block", i+1)
		}
	}
}

func TestChunkPreservesContentUpToTheLimit(t *testing.T) {
	body := linesOfSize(2 * ChunkTarget)
	parts := Chunk(body)
	if len(parts) < 2 || len(parts) > MaxParts {
		t.Fatalf("got %d parts", len(parts))
	}
	// Every non-blank source line survives somewhere, in order.
	joined := strings.Join(parts, "\n")
	for _, line := range strings.Split(body, "\n") {
		if line == "" {
			continue
		}
		if !strings.Contains(joined, line) {
			t.Fatalf("line %q was lost", line)
		}
	}
}

// linesOfSize builds a body of exactly n characters, laid out in short lines
// the way a rendered ops message is.
func linesOfSize(n int) string {
	var b strings.Builder
	i := 0
	for b.Len() < n {
		if remaining := n - b.Len(); remaining <= 40 {
			b.WriteString(strings.Repeat("x", remaining))
			break
		}
		line := "svc-" + string(rune('a'+i%26)) + " " + strings.Repeat("y", 30)
		b.WriteString(line)
		b.WriteString("\n")
		i++
	}
	return b.String()
}

// assertNumbered checks the "(i/n)" headers.
func assertNumbered(t *testing.T, parts []string) {
	t.Helper()
	for i, p := range parts {
		want := "(" + string(rune('0'+i+1)) + "/" + string(rune('0'+len(parts))) + ")\n"
		if !strings.HasPrefix(p, want) {
			t.Fatalf("part %d does not start with %q: %q", i+1, want, p[:min(20, len(p))])
		}
	}
}

// assertValidHTMLPart checks that a part is independently parseable: balanced
// tags, no tag or entity cut in half.
func assertValidHTMLPart(t *testing.T, part string) {
	t.Helper()
	assertNoSplitEntities(t, part)

	var stack []string
	for i := 0; i < len(part); {
		if part[i] != '<' {
			i++
			continue
		}
		j := strings.IndexByte(part[i:], '>')
		if j < 0 {
			t.Fatalf("part ends inside a tag: %q", part[max(0, len(part)-40):])
		}
		inner := part[i+1 : i+j]
		i += j + 1
		switch {
		case inner == "" || strings.HasSuffix(inner, "/"):
		case inner[0] == '/':
			name := inner[1:]
			if len(stack) == 0 || stack[len(stack)-1] != name {
				t.Fatalf("unbalanced </%s> in part:\n%s", name, part[:min(200, len(part))])
			}
			stack = stack[:len(stack)-1]
		default:
			name := inner
			if sp := strings.IndexByte(inner, ' '); sp >= 0 {
				name = inner[:sp]
			}
			stack = append(stack, name)
		}
	}
	if len(stack) != 0 {
		t.Fatalf("part left %v open:\n%s", stack, part[max(0, len(part)-200):])
	}
}

// assertNoSplitEntities checks that every "&" begins a complete entity.
func assertNoSplitEntities(t *testing.T, s string) {
	t.Helper()
	for i := 0; i < len(s); i++ {
		if s[i] != '&' {
			continue
		}
		end := strings.IndexByte(s[i:], ';')
		if end < 0 || end > 12 {
			t.Fatalf("truncated entity at offset %d: %q", i, s[i:min(i+16, len(s))])
		}
	}
}
