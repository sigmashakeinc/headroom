package main

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
)

// DefaultMaxLines is the default ceiling for log/text truncation.
const DefaultMaxLines = 500

// importanceRe matches lines that must never be dropped during truncation.
var importanceRe = regexp.MustCompile(`(?i)(error|warn|fail|fatal|panic|denied|refus|exception|cannot|undefined)`)

// Compress applies content-type–aware compression to input. kind must be one
// of "auto", "json", "code", "log", "text"; "auto" triggers detection.
// maxLines controls the log/text truncation ceiling (<=0 uses DefaultMaxLines).
// Returns the compressed bytes and the kind actually applied.
func Compress(input []byte, kind string, maxLines int) (out []byte, detectedKind string) {
	if maxLines <= 0 {
		maxLines = DefaultMaxLines
	}
	if kind == "" || kind == "auto" {
		kind = detectKind(input)
	}
	detectedKind = kind
	switch kind {
	case "json":
		out = compressJSON(input)
	case "code":
		out = compressCode(input)
	default: // "log", "text"
		out = compressLogText(input, maxLines)
	}
	return
}

// detectKind sniffs the content type of input. Returns "json", "code", or "text".
func detectKind(input []byte) string {
	trimmed := bytes.TrimSpace(input)
	// JSON: must start with { or [ and pass json.Valid.
	if len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[') && json.Valid(trimmed) {
		return "json"
	}
	// Code heuristic: keyword or brace/semicolon density per line.
	s := string(input)
	lines := strings.Split(s, "\n")
	n := len(lines) // always >= 1 from strings.Split
	codeKw := 0
	for _, kw := range []string{
		"func ", "def ", "import ", "class ", "return ",
		"package ", "var ", "const ", "fn ", "pub ",
	} {
		codeKw += strings.Count(s, kw)
	}
	braces := strings.Count(s, "{") + strings.Count(s, "}") + strings.Count(s, ";")
	// Heuristic: 1 keyword per 20 lines OR 1 brace/semicolon per 10 lines.
	if codeKw*20 >= n || braces*10 >= n {
		return "code"
	}
	return "text"
}

// --- JSON compressor ---

const (
	jsonMaxArrayElems = 20  // max array elements kept in a compressed digest
	jsonMaxStrLen     = 200 // max string length kept in a compressed digest
)

// compressJSON compact-marshals JSON and truncates long arrays/strings.
// Falls back to compressLogText if the input is not valid JSON.
// Note: json.Marshal on data produced by json.Unmarshal cannot error, so
// there is no defensive fallback for that path.
func compressJSON(input []byte) []byte {
	var v interface{}
	if err := json.Unmarshal(input, &v); err != nil {
		// Not valid JSON despite detectKind saying so — fall back to text.
		return compressLogText(input, DefaultMaxLines)
	}
	out, _ := json.Marshal(truncateJSONValue(v))
	return out
}

// truncateJSONValue recursively trims long arrays and strings.
func truncateJSONValue(v interface{}) interface{} {
	switch val := v.(type) {
	case map[string]interface{}:
		out := make(map[string]interface{}, len(val))
		for k, elem := range val {
			out[k] = truncateJSONValue(elem)
		}
		return out
	case []interface{}:
		if len(val) > jsonMaxArrayElems {
			trimmed := make([]interface{}, jsonMaxArrayElems+1)
			for i := 0; i < jsonMaxArrayElems; i++ {
				trimmed[i] = truncateJSONValue(val[i])
			}
			trimmed[jsonMaxArrayElems] = "…(" + itoa(len(val)-jsonMaxArrayElems) + " more)…"
			return trimmed
		}
		out := make([]interface{}, len(val))
		for i, elem := range val {
			out[i] = truncateJSONValue(elem)
		}
		return out
	case string:
		if len(val) > jsonMaxStrLen {
			return val[:jsonMaxStrLen] + "…"
		}
		return val
	default:
		return v
	}
}

// --- Code compressor ---

// compressCode applies language-agnostic lexical reduction:
//   - drops full-line comments (//, #, --, and leading ;)
//   - collapses blank-line runs
//   - strips trailing whitespace
//
// Conservative: a line is only dropped if its entire trimmed content is a
// comment prefix. Lines containing a comment token anywhere other than the
// very start (e.g. `x = "// inside"`) are always kept.
func compressCode(input []byte) []byte {
	lines := strings.Split(string(input), "\n")
	out := make([]string, 0, len(lines))
	prevBlank := false
	for _, l := range lines {
		stripped := strings.TrimSpace(l)
		if isFullLineComment(stripped) {
			continue
		}
		rStripped := strings.TrimRight(l, " \t")
		isBlank := stripped == ""
		if isBlank && prevBlank {
			continue // collapse blank run
		}
		out = append(out, rStripped)
		prevBlank = isBlank
	}
	return []byte(strings.Join(out, "\n"))
}

// isFullLineComment returns true if the entire trimmed line is a comment.
// Recognises leading //, #, --, ; only.
func isFullLineComment(s string) bool {
	return s != "" && (strings.HasPrefix(s, "//") ||
		strings.HasPrefix(s, "#") ||
		strings.HasPrefix(s, "--") ||
		strings.HasPrefix(s, ";"))
}

// --- Log / text compressor ---
//
// Re-implements STK's pipeline (~30 LOC core), compactly, without importing
// or exec-ing the stk binary. Decision: reimplementing avoids coupling two
// standalone stdlib tools via a replace directive and removes a hard runtime
// dependency on the stk binary being installed.
//
// Pipeline (in order):
//  1. CR strip
//  2. Consecutive-duplicate collapse → (×N)
//  3. Blank-run collapse
//  4. Importance-preserving truncation

func compressLogText(input []byte, maxLines int) []byte {
	if len(input) == 0 {
		return input
	}
	lines := strings.Split(string(input), "\n")
	lines = ltStripCR(lines)
	lines = ltCollapseDups(lines)
	lines = ltCollapseBlankRuns(lines)
	lines = ltTruncate(lines, maxLines)
	return []byte(strings.Join(lines, "\n"))
}

func ltStripCR(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		if idx := strings.LastIndex(l, "\r"); idx >= 0 {
			l = l[idx+1:]
		}
		out = append(out, l)
	}
	return out
}

func ltCollapseDups(lines []string) []string {
	if len(lines) == 0 {
		return lines
	}
	out := make([]string, 0, len(lines))
	prev := lines[0]
	count := 1
	for _, l := range lines[1:] {
		if l == prev {
			count++
		} else {
			out = append(out, ltFormatDup(prev, count))
			prev = l
			count = 1
		}
	}
	out = append(out, ltFormatDup(prev, count))
	return out
}

func ltFormatDup(line string, count int) string {
	if count > 1 {
		return line + " (×" + itoa(count) + ")"
	}
	return line
}

func ltCollapseBlankRuns(lines []string) []string {
	out := make([]string, 0, len(lines))
	prevBlank := false
	for _, l := range lines {
		isBlank := strings.TrimSpace(l) == ""
		if isBlank && prevBlank {
			continue
		}
		out = append(out, l)
		prevBlank = isBlank
	}
	return out
}

type ltSlot struct {
	origIdx int
	line    string
}

func ltTruncate(lines []string, maxLines int) []string {
	if len(lines) <= maxLines {
		return lines
	}
	var important []ltSlot
	var normal []ltSlot
	for i, l := range lines {
		if importanceRe.MatchString(l) {
			important = append(important, ltSlot{i, l})
		} else {
			normal = append(normal, ltSlot{i, l})
		}
	}
	// If importance lines alone fill/exceed the budget, keep them all with a
	// leading marker for the dropped normal lines.
	if len(important) >= maxLines {
		out := make([]string, 0, len(important)+1)
		if trimmed := len(lines) - len(important); trimmed > 0 {
			out = append(out, ltMarker(trimmed))
		}
		for _, s := range important {
			out = append(out, s.line)
		}
		return out
	}
	// Budget of non-important lines we can keep.
	budget := maxLines - len(important)
	var keptNormal []ltSlot
	trimCount := 0
	if len(normal) <= budget {
		keptNormal = normal
	} else {
		headN := budget * 2 / 3
		if headN < 1 {
			headN = 1
		}
		tailN := budget - headN
		trimCount = len(normal) - budget
		keptNormal = make([]ltSlot, 0, budget)
		keptNormal = append(keptNormal, normal[:headN]...)
		if tailN > 0 {
			keptNormal = append(keptNormal, normal[len(normal)-tailN:]...)
		}
	}
	// Merge importance + kept-normal, sort by original index.
	merged := make([]ltSlot, 0, len(important)+len(keptNormal))
	merged = append(merged, important...)
	merged = append(merged, keptNormal...)
	ltSortSlots(merged)
	// Emit; insert trimmed marker once at the first index gap caused by removals.
	out := make([]string, 0, len(merged)+1)
	markerInserted := false
	prevIdx := -1
	for _, s := range merged {
		if !markerInserted && trimCount > 0 && prevIdx >= 0 && s.origIdx > prevIdx+1 {
			out = append(out, ltMarker(trimCount))
			markerInserted = true
		}
		out = append(out, s.line)
		prevIdx = s.origIdx
	}
	if !markerInserted && trimCount > 0 {
		out = append(out, ltMarker(trimCount))
	}
	return out
}

func ltMarker(m int) string {
	return "… [headroom] trimmed " + itoa(m) + " line(s) …"
}

func ltSortSlots(s []ltSlot) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j].origIdx < s[j-1].origIdx; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// itoa converts a non-negative int to a decimal string without importing strconv.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	buf := [20]byte{}
	pos := len(buf)
	for n > 0 {
		pos--
		buf[pos] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[pos:])
}
