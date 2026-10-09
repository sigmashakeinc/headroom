package main

import (
	"strings"
	"testing"
)

// --- detectKind ---

func TestDetectKind_JSON_Object(t *testing.T) {
	if got := detectKind([]byte(`{"a":1}`)); got != "json" {
		t.Errorf("got %q, want json", got)
	}
}

func TestDetectKind_JSON_Array(t *testing.T) {
	if got := detectKind([]byte(`[1,2,3]`)); got != "json" {
		t.Errorf("got %q, want json", got)
	}
}

func TestDetectKind_JSON_WithWhitespace(t *testing.T) {
	if got := detectKind([]byte("  {\n  \"key\": \"value\"\n}\n")); got != "json" {
		t.Errorf("got %q, want json", got)
	}
}

func TestDetectKind_Code_Go(t *testing.T) {
	src := "package main\n\nfunc main() {\n\treturn\n}\n"
	if got := detectKind([]byte(src)); got != "code" {
		t.Errorf("got %q, want code", got)
	}
}

func TestDetectKind_Code_Python(t *testing.T) {
	src := "def foo():\n    return 42\n\nclass Bar:\n    pass\n"
	if got := detectKind([]byte(src)); got != "code" {
		t.Errorf("got %q, want code", got)
	}
}

func TestDetectKind_Text_Log(t *testing.T) {
	log := "2024-01-01 INFO starting server\n2024-01-01 INFO listening on :8080\n"
	if got := detectKind([]byte(log)); got != "text" {
		t.Errorf("got %q, want text", got)
	}
}

func TestDetectKind_Empty(t *testing.T) {
	// Empty input should not panic and should return "text" or similar.
	got := detectKind([]byte{})
	if got == "" {
		t.Errorf("detectKind(empty) returned empty string")
	}
}

func TestDetectKind_InvalidJSONWithBrace(t *testing.T) {
	// Starts with { but is not valid JSON.
	got := detectKind([]byte("{not valid json}"))
	if got == "json" {
		t.Errorf("invalid JSON should not be detected as json, got %q", got)
	}
}

// --- compressJSON ---

func TestCompressJSON_Minify(t *testing.T) {
	input := []byte(`{"a":  1,  "b":  [1, 2, 3]}`)
	out := compressJSON(input)
	s := string(out)
	if strings.Contains(s, "  ") {
		t.Errorf("minified JSON should have no double-spaces: %q", s)
	}
	if !strings.Contains(s, `"a":1`) {
		t.Errorf("expected compact key:value, got %q", s)
	}
}

func TestCompressJSON_LongArray(t *testing.T) {
	// Build an array of 30 elements; compressJSON should truncate after 20.
	elems := make([]byte, 0, 128)
	elems = append(elems, '[')
	for i := 0; i < 30; i++ {
		if i > 0 {
			elems = append(elems, ',')
		}
		elems = append(elems, []byte(itoa(i))...)
	}
	elems = append(elems, ']')
	out := compressJSON(elems)
	s := string(out)
	// Should contain truncation marker.
	if !strings.Contains(s, "more") {
		t.Errorf("expected truncation marker for 30-element array, got %q", s)
	}
	// Should contain "10 more" (30-20=10).
	if !strings.Contains(s, "10 more") {
		t.Errorf("expected '10 more' in truncated array, got %q", s)
	}
}

func TestCompressJSON_LongString(t *testing.T) {
	// A string of 300 chars should be truncated to ≤201 + "…".
	long := strings.Repeat("x", 300)
	input := []byte(`{"key":"` + long + `"}`)
	out := compressJSON(input)
	s := string(out)
	if !strings.Contains(s, "…") {
		t.Errorf("expected truncation ellipsis for 300-char string, got %q", s)
	}
}

func TestCompressJSON_Nested(t *testing.T) {
	input := []byte(`{"outer": {"inner": "value"}, "num": 42}`)
	out := compressJSON(input)
	if !strings.Contains(string(out), "inner") {
		t.Errorf("nested map key should be preserved, got %q", string(out))
	}
}

func TestCompressJSON_MalformedFallsBackToText(t *testing.T) {
	// Invalid JSON passed directly to compressJSON should not panic.
	input := []byte("{this is not json}")
	out := compressJSON(input)
	// Falls back to compressLogText; result is non-nil.
	if out == nil {
		t.Error("compressJSON(malformed) returned nil")
	}
}

func TestCompressJSON_EmptyObject(t *testing.T) {
	input := []byte(`{}`)
	out := compressJSON(input)
	if string(out) != "{}" {
		t.Errorf("empty object: got %q, want {}", string(out))
	}
}

func TestCompressJSON_EmptyArray(t *testing.T) {
	input := []byte(`[]`)
	out := compressJSON(input)
	if string(out) != "[]" {
		t.Errorf("empty array: got %q, want []", string(out))
	}
}

// --- compressCode ---

func TestCompressCode_DropSlashSlashComment(t *testing.T) {
	input := "// This is a comment\nfunc main() {}\n"
	out := string(compressCode([]byte(input)))
	if strings.Contains(out, "This is a comment") {
		t.Errorf("// comment should be dropped, got %q", out)
	}
	if !strings.Contains(out, "func main()") {
		t.Errorf("code line should be kept, got %q", out)
	}
}

func TestCompressCode_DropHashComment(t *testing.T) {
	input := "# python comment\nx = 42\n"
	out := string(compressCode([]byte(input)))
	if strings.Contains(out, "python comment") {
		t.Errorf("# comment should be dropped, got %q", out)
	}
	if !strings.Contains(out, "x = 42") {
		t.Errorf("code line should be kept, got %q", out)
	}
}

func TestCompressCode_DropDoubleDashComment(t *testing.T) {
	input := "-- SQL comment\nSELECT 1;\n"
	out := string(compressCode([]byte(input)))
	if strings.Contains(out, "SQL comment") {
		t.Errorf("-- comment should be dropped, got %q", out)
	}
}

func TestCompressCode_DropSemicolonComment(t *testing.T) {
	input := "; Lisp comment\n(defun foo ())\n"
	out := string(compressCode([]byte(input)))
	if strings.Contains(out, "Lisp comment") {
		t.Errorf("; comment should be dropped, got %q", out)
	}
}

func TestCompressCode_BlankRunsCollapsed(t *testing.T) {
	input := "code1\n\n\n\ncode2\n"
	out := string(compressCode([]byte(input)))
	// Trim trailing newline before splitting to avoid counting the artifact
	// empty element that strings.Split produces after a final '\n'.
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	blanks := 0
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			blanks++
		}
	}
	if blanks > 1 {
		t.Errorf("blank runs should collapse to one blank, got %d blanks in %q", blanks, out)
	}
}

func TestCompressCode_TrailingWhitespaceStripped(t *testing.T) {
	input := "func foo()   \n"
	out := string(compressCode([]byte(input)))
	// Lines should not have trailing spaces.
	for _, l := range strings.Split(out, "\n") {
		if strings.TrimRight(l, " \t") != l {
			t.Errorf("trailing whitespace not stripped in line %q", l)
		}
	}
}

// Negative case: a line containing // inside a string literal must NOT be dropped.
func TestCompressCode_StringLiteralWithCommentTokenKept(t *testing.T) {
	input := `x := "// this is inside a string"` + "\ny := 42\n"
	out := string(compressCode([]byte(input)))
	if !strings.Contains(out, "inside a string") {
		t.Errorf("line with // inside a string literal should NOT be dropped, got %q", out)
	}
}

func TestCompressCode_AllComments(t *testing.T) {
	input := "// comment1\n# comment2\n-- comment3\n; comment4\n"
	out := string(compressCode([]byte(input)))
	// Only blank/empty output expected.
	if strings.TrimSpace(out) != "" {
		t.Errorf("all-comment input: expected empty output, got %q", out)
	}
}

// --- log/text pipeline (ltStripCR, ltCollapseDups, ltCollapseBlankRuns, ltTruncate) ---

func TestLtStripCR_Basic(t *testing.T) {
	got := ltStripCR([]string{"spin\rfinal", "next"})
	if got[0] != "final" {
		t.Errorf("CR strip: got %q, want %q", got[0], "final")
	}
}

func TestLtStripCR_MultipleCR(t *testing.T) {
	got := ltStripCR([]string{"a\rb\rc\rlast"})
	if got[0] != "last" {
		t.Errorf("multiple CRs: got %q, want last", got[0])
	}
}

func TestLtCollapseDups_Basic(t *testing.T) {
	got := ltCollapseDups([]string{"x", "x", "x"})
	if len(got) != 1 || got[0] != "x (×3)" {
		t.Errorf("dup collapse: got %v", got)
	}
}

func TestLtCollapseDups_NonAdjacent(t *testing.T) {
	got := ltCollapseDups([]string{"a", "b", "a"})
	if len(got) != 3 {
		t.Errorf("non-adjacent dups should not collapse: %v", got)
	}
}

func TestLtCollapseBlankRuns_Basic(t *testing.T) {
	got := ltCollapseBlankRuns([]string{"a", "", "", "b"})
	blanks := 0
	for _, l := range got {
		if l == "" {
			blanks++
		}
	}
	if blanks != 1 {
		t.Errorf("expected 1 blank line, got %d in %v", blanks, got)
	}
}

func TestLtTruncate_NoOp(t *testing.T) {
	lines := makeNLines(10, "line")
	got := ltTruncate(lines, 100)
	if len(got) != 10 {
		t.Errorf("no-op: got %d lines, want 10", len(got))
	}
}

func TestLtTruncate_MarkerInserted(t *testing.T) {
	lines := makeNLines(100, "normal")
	got := ltTruncate(lines, 10)
	hasMarker := false
	for _, l := range got {
		if strings.Contains(l, "[headroom] trimmed") {
			hasMarker = true
		}
	}
	if !hasMarker {
		t.Errorf("truncation marker not found in output: %v", got)
	}
}

func TestLtTruncate_ImportanceLinesAlwaysKept(t *testing.T) {
	lines := makeNLines(200, "normal")
	lines[50] = "ERROR: something went wrong"
	lines[100] = "FATAL: critical failure"
	got := ltTruncate(lines, 10)
	foundErr, foundFatal := false, false
	for _, l := range got {
		if l == "ERROR: something went wrong" {
			foundErr = true
		}
		if l == "FATAL: critical failure" {
			foundFatal = true
		}
	}
	if !foundErr {
		t.Error("ERROR line was dropped by truncation")
	}
	if !foundFatal {
		t.Error("FATAL line was dropped by truncation")
	}
}

func TestLtTruncate_OnlyImportanceLines(t *testing.T) {
	lines := []string{"error: a", "warn: b", "fatal: c"}
	got := ltTruncate(lines, 2)
	count := 0
	for _, l := range got {
		if strings.HasPrefix(l, "error:") || strings.HasPrefix(l, "warn:") || strings.HasPrefix(l, "fatal:") {
			count++
		}
	}
	if count != 3 {
		t.Errorf("expected all 3 importance lines, got %d in %v", count, got)
	}
}

// --- Compress (integration) ---

func TestCompress_Empty(t *testing.T) {
	out, kind := Compress([]byte{}, "auto", 0)
	if len(out) != 0 {
		t.Errorf("empty input: got non-empty output %q", out)
	}
	if kind == "" {
		t.Error("empty input: kind should not be empty")
	}
}

func TestCompress_JSON_AutoDetect(t *testing.T) {
	input := []byte(`{"a":  1,  "b": 2}`)
	out, kind := Compress(input, "auto", 0)
	if kind != "json" {
		t.Errorf("expected kind=json, got %q", kind)
	}
	if len(out) >= len(input) {
		// Compact form should be smaller or equal.
		t.Logf("note: compressed JSON %d >= input %d (acceptable for tiny objects)", len(out), len(input))
	}
}

func TestCompress_Code_ExplicitType(t *testing.T) {
	input := []byte("// header\nfunc main() {}\n// footer\n")
	out, kind := Compress(input, "code", 0)
	if kind != "code" {
		t.Errorf("expected kind=code, got %q", kind)
	}
	if strings.Contains(string(out), "header") {
		t.Errorf("comment should be stripped, got %q", string(out))
	}
}

func TestCompress_Log_DuplicateCollapse(t *testing.T) {
	input := []byte("INFO: starting\nINFO: starting\nINFO: starting\nDone\n")
	out, _ := Compress(input, "log", 0)
	if !strings.Contains(string(out), "(×3)") {
		t.Errorf("log duplicates should collapse, got %q", string(out))
	}
}

func TestCompress_Log_CRStrip(t *testing.T) {
	input := []byte("spin\rfinal\nnext\n")
	out, _ := Compress(input, "log", 0)
	if strings.Contains(string(out), "spin") {
		t.Errorf("CR-overwritten segment should be stripped, got %q", string(out))
	}
	if !strings.Contains(string(out), "final") {
		t.Errorf("final segment should be kept, got %q", string(out))
	}
}

func TestCompress_MaxLinesHonored(t *testing.T) {
	// 300 unique lines, --max 20, text type.
	var sb strings.Builder
	for i := 0; i < 300; i++ {
		sb.WriteString("line " + itoa(i) + "\n")
	}
	out, _ := Compress([]byte(sb.String()), "text", 20)
	lines := strings.Split(strings.TrimRight(string(out), "\n"), "\n")
	// 20 content lines + 1 marker = 21 max.
	if len(lines) > 21 {
		t.Errorf("expected ≤21 lines with max=20, got %d", len(lines))
	}
}

func TestCompress_MalformedJSON_NoPanic(t *testing.T) {
	// Should not panic; falls back to text.
	input := []byte("{broken json: yes}")
	out, _ := Compress(input, "auto", 0)
	if out == nil {
		t.Error("malformed JSON auto: got nil output")
	}
}

func TestCompress_TextExplicit(t *testing.T) {
	input := []byte("hello\nhello\nhello\n")
	out, kind := Compress(input, "text", 0)
	if kind != "text" {
		t.Errorf("expected kind=text, got %q", kind)
	}
	if !strings.Contains(string(out), "(×3)") {
		t.Errorf("text dup collapse expected, got %q", string(out))
	}
}

// --- itoa ---

func TestItoa(t *testing.T) {
	cases := []struct {
		n    int
		want string
	}{
		{0, "0"},
		{1, "1"},
		{9, "9"},
		{10, "10"},
		{99, "99"},
		{100, "100"},
		{1000000, "1000000"},
	}
	for _, tc := range cases {
		if got := itoa(tc.n); got != tc.want {
			t.Errorf("itoa(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}

// --- isFullLineComment ---

func TestIsFullLineComment(t *testing.T) {
	cases := []struct {
		s    string
		want bool
	}{
		{"// comment", true},
		{"# hash", true},
		{"-- dash", true},
		{"; semicolon", true},
		{"x = 42", false},
		{"", false},
		{`x := "// not a comment"`, false},
		{"   // indented (not trimmed)", false}, // detectKind receives trimmed input
		{"//", true},
	}
	for _, tc := range cases {
		got := isFullLineComment(tc.s)
		if got != tc.want {
			t.Errorf("isFullLineComment(%q) = %v, want %v", tc.s, got, tc.want)
		}
	}
}

// --- ltCollapseDups edge cases ---

func TestLtCollapseDups_Empty(t *testing.T) {
	got := ltCollapseDups([]string{})
	if len(got) != 0 {
		t.Errorf("empty slice: got %v, want []", got)
	}
}

// --- ltTruncate coverage: importance fills budget (leading marker) ---

func TestLtTruncate_ImportanceFillsBudget(t *testing.T) {
	// 5 lines, 2 are importance, maxLines=2 so importance alone fills budget.
	// 3 normal lines are dropped → leading marker must appear.
	lines := []string{"normal 0", "error: a", "normal 1", "warn: b", "normal 2"}
	got := ltTruncate(lines, 2)
	hasMarker := false
	for _, l := range got {
		if strings.Contains(l, "[headroom] trimmed") {
			hasMarker = true
		}
	}
	if !hasMarker {
		t.Errorf("expected leading trim marker when importance fills budget, got %v", got)
	}
	// All importance lines must still be present.
	foundErr, foundWarn := false, false
	for _, l := range got {
		if l == "error: a" {
			foundErr = true
		}
		if l == "warn: b" {
			foundWarn = true
		}
	}
	if !foundErr || !foundWarn {
		t.Errorf("importance lines missing: error=%v warn=%v in %v", foundErr, foundWarn, got)
	}
}

// --- ltTruncate coverage: tail-gap marker (all removed lines are at the end) ---

func TestLtTruncate_TailGapMarker(t *testing.T) {
	// 50 normal lines, maxLines=1 → headN=1, tailN=0; all 49 removals at the tail.
	// The marker must be appended AFTER the last kept line (tail-gap path).
	lines := makeNLines(50, "n")
	got := ltTruncate(lines, 1)
	if len(got) == 0 {
		t.Fatal("ltTruncate returned empty slice")
	}
	last := got[len(got)-1]
	if !strings.Contains(last, "[headroom] trimmed") {
		t.Errorf("tail-gap marker should be the last line, got last=%q in %v", last, got)
	}
}

// --- helpers ---

func makeNLines(n int, prefix string) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = prefix + " " + itoa(i)
	}
	return out
}
