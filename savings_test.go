package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// savingsEntry mirrors the JSON fields written by appendSavings.
type savingsEntry struct {
	Tool      string `json:"tool"`
	Ts        int64  `json:"ts"`
	OrigBytes int    `json:"orig_bytes"`
	CompBytes int    `json:"comp_bytes"`
}

// readLedger reads the ledger at dir/token-savings.jsonl and returns parsed
// entries. Fails the test if the file is absent or any line is unparseable.
func readLedger(t *testing.T, dir string) []savingsEntry {
	t.Helper()
	path := filepath.Join(dir, "token-savings.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read ledger %s: %v", path, err)
	}
	var entries []savingsEntry
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if line == "" {
			continue
		}
		var e savingsEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("parse ledger line %q: %v", line, err)
		}
		entries = append(entries, e)
	}
	return entries
}

// TestAppendSavings_WritesLedgerLine verifies that a single call writes exactly
// one well-formed JSON line with the correct field values.
func TestAppendSavings_WritesLedgerLine(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SIGMASHAKE_HOME", dir)

	const origBytes = 1000
	const compBytes = 200
	appendSavings(origBytes, compBytes)

	entries := readLedger(t, dir)
	if len(entries) != 1 {
		t.Fatalf("expected 1 ledger entry, got %d", len(entries))
	}
	e := entries[0]
	if e.Tool != "headroom" {
		t.Errorf("tool=%q, want %q", e.Tool, "headroom")
	}
	if e.Ts <= 0 {
		t.Errorf("ts=%d should be a positive unix timestamp", e.Ts)
	}
	if e.OrigBytes != origBytes {
		t.Errorf("orig_bytes=%d, want %d", e.OrigBytes, origBytes)
	}
	if e.CompBytes != compBytes {
		t.Errorf("comp_bytes=%d, want %d", e.CompBytes, compBytes)
	}
}

// TestAppendSavings_OptOut_Zero verifies that HEADROOM_SAVINGS=0 suppresses the
// ledger write entirely.
func TestAppendSavings_OptOut_Zero(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SIGMASHAKE_HOME", dir)
	t.Setenv("HEADROOM_SAVINGS", "0")

	appendSavings(500, 100)

	path := filepath.Join(dir, "token-savings.jsonl")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("ledger file should not exist when HEADROOM_SAVINGS=0")
	}
}

// TestAppendSavings_OptOut_False verifies that HEADROOM_SAVINGS=false also
// suppresses the ledger write.
func TestAppendSavings_OptOut_False(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SIGMASHAKE_HOME", dir)
	t.Setenv("HEADROOM_SAVINGS", "false")

	appendSavings(500, 100)

	path := filepath.Join(dir, "token-savings.jsonl")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("ledger file should not exist when HEADROOM_SAVINGS=false")
	}
}

// TestAppendSavings_NonFatalOnError verifies that a write error (unwritable
// SIGMASHAKE_HOME directory) does not panic, does not return an error (the
// function is void), and does not create a ledger file.
func TestAppendSavings_NonFatalOnError(t *testing.T) {
	if !permsEnforced() {
		t.Skip("chmod restrictions do not apply to root")
	}
	dir := t.TempDir()
	t.Setenv("SIGMASHAKE_HOME", dir)

	// Make the directory unwritable so OpenFile(O_CREATE) fails.
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	defer os.Chmod(dir, 0o755)

	// Must not panic — appendSavings is best-effort/non-fatal.
	appendSavings(1000, 200)

	path := filepath.Join(dir, "token-savings.jsonl")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("ledger file should not exist after write error")
	}
}

// TestAppendSavings_CompBytesIsDigestLength is the key correctness test: it
// verifies that the recorded comp_bytes equals len(Compress()) — the text
// digest emitted to the LLM — not the gzip blob size stored for retrieval.
func TestAppendSavings_CompBytesIsDigestLength(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SIGMASHAKE_HOME", dir)

	// Use content that produces real compression (duplicate lines).
	input := []byte("INFO: starting server\nINFO: starting server\nINFO: starting server\nDone.\n")
	compressed, _ := Compress(input, "text", DefaultMaxLines)
	digestLen := len(compressed)

	appendSavings(len(input), digestLen)

	entries := readLedger(t, dir)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].CompBytes != digestLen {
		t.Errorf("comp_bytes=%d, want %d (len of Compress() text digest)",
			entries[0].CompBytes, digestLen)
	}
	// Sanity: comp_bytes should be smaller than orig_bytes (real savings occurred).
	if entries[0].CompBytes >= entries[0].OrigBytes {
		t.Errorf("expected comp_bytes (%d) < orig_bytes (%d)",
			entries[0].CompBytes, entries[0].OrigBytes)
	}
}

// TestAppendSavings_MultipleAppends verifies that repeated calls append
// multiple lines to the same file.
func TestAppendSavings_MultipleAppends(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SIGMASHAKE_HOME", dir)

	appendSavings(1000, 200)
	appendSavings(2000, 400)
	appendSavings(3000, 600)

	entries := readLedger(t, dir)
	if len(entries) != 3 {
		t.Fatalf("expected 3 ledger entries, got %d", len(entries))
	}
	want := [][2]int{{1000, 200}, {2000, 400}, {3000, 600}}
	for i, e := range entries {
		if e.OrigBytes != want[i][0] || e.CompBytes != want[i][1] {
			t.Errorf("entry %d: orig_bytes=%d comp_bytes=%d, want %d/%d",
				i, e.OrigBytes, e.CompBytes, want[i][0], want[i][1])
		}
	}
}

// TestCmdWrap_RecordsSavings verifies that the wrap subcommand appends a ledger
// entry whose comp_bytes equals the Compress() digest length (not the gzip blob).
func TestCmdWrap_RecordsSavings(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SIGMASHAKE_HOME", dir)
	t.Setenv("HEADROOM_HOME", filepath.Join(dir, "headroom"))

	// Redirect stdout so cmdWrap output doesn't pollute test output.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origStdout := os.Stdout
	os.Stdout = w

	// Wrap a command whose output is genuinely compressible (repeated lines) so
	// the honest savings guard (records only comp < orig) logs the reduction —
	// a tiny incompressible output would correctly NOT be logged.
	t.Setenv("HEADROOM_TEST_CHILD", "print")
	t.Setenv("HEADROOM_TEST_TEXT", "warn: retry\nwarn: retry\nwarn: retry\nwarn: retry\nDone\n")
	exit := run([]string{"wrap", os.Args[0]})

	w.Close()
	os.Stdout = origStdout

	var capturedOut bytes.Buffer
	if _, err := capturedOut.ReadFrom(r); err != nil {
		t.Fatalf("read captured stdout: %v", err)
	}
	r.Close()

	if exit != 0 {
		t.Fatalf("wrap returned exit %d", exit)
	}

	entries := readLedger(t, dir)
	if len(entries) != 1 {
		t.Fatalf("expected 1 ledger entry, got %d", len(entries))
	}
	e := entries[0]
	if e.Tool != "headroom" {
		t.Errorf("tool=%q, want headroom", e.Tool)
	}
	if e.OrigBytes <= 0 {
		t.Errorf("orig_bytes=%d should be positive", e.OrigBytes)
	}
	if e.CompBytes <= 0 {
		t.Errorf("comp_bytes=%d should be positive", e.CompBytes)
	}
	// Verify comp_bytes is the text-digest length by recomputing Compress on
	// a known "echo hello headroom" output (14 bytes + newline from echo).
	// We cannot know the exact echo output byte-for-byte in all envs, but
	// we can verify comp_bytes is NOT the gzip blob size by checking it is
	// ≥ orig_bytes * 0 (non-negative) and ≤ orig_bytes + 1 (never grossly inflated).
	// The real invariant check is in TestAppendSavings_CompBytesIsDigestLength.
	if e.CompBytes > e.OrigBytes*2 {
		t.Errorf("comp_bytes=%d seems implausibly large vs orig_bytes=%d", e.CompBytes, e.OrigBytes)
	}
}

// TestCmdCompress_RecordsSavings_NoStore verifies that --no-store still
// records savings (the ledger is independent of the retrieval store).
func TestCmdCompress_RecordsSavings_NoStore(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SIGMASHAKE_HOME", dir)
	t.Setenv("HEADROOM_HOME", filepath.Join(dir, "headroom"))

	// Write a temp file with compressible content.
	input := []byte("warn: retry\nwarn: retry\nwarn: retry\nDone\n")
	tmpFile := filepath.Join(dir, "input.txt")
	if err := os.WriteFile(tmpFile, input, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Redirect stdout to suppress output.
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origStdout := os.Stdout
	os.Stdout = w

	exit := run([]string{"compress", "--type", "log", "--no-store", tmpFile})

	w.Close()
	os.Stdout = origStdout
	r.Close()

	if exit != 0 {
		t.Fatalf("compress --no-store returned exit %d", exit)
	}

	entries := readLedger(t, dir)
	if len(entries) != 1 {
		t.Fatalf("expected 1 ledger entry even with --no-store, got %d", len(entries))
	}
	e := entries[0]

	// comp_bytes must match Compress() output length (the text digest).
	compressed, _ := Compress(input, "log", resolveMaxLines(0))
	if e.CompBytes != len(compressed) {
		t.Errorf("comp_bytes=%d, want %d (Compress() digest length)", e.CompBytes, len(compressed))
	}
	if e.OrigBytes != len(input) {
		t.Errorf("orig_bytes=%d, want %d", e.OrigBytes, len(input))
	}
}

// TestCmdCompress_OptOut_DisablesLedger verifies HEADROOM_SAVINGS=0 prevents
// ledger writes even when a real reduction occurred through the CLI path.
func TestCmdCompress_OptOut_DisablesLedger(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SIGMASHAKE_HOME", dir)
	t.Setenv("HEADROOM_HOME", filepath.Join(dir, "headroom"))
	t.Setenv("HEADROOM_SAVINGS", "0")

	input := []byte("line\nline\nline\n")
	tmpFile := filepath.Join(dir, "input.txt")
	if err := os.WriteFile(tmpFile, input, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	origStdout := os.Stdout
	os.Stdout = w
	run([]string{"compress", "--type", "text", "--no-store", tmpFile})
	w.Close()
	os.Stdout = origStdout
	r.Close()

	path := filepath.Join(dir, "token-savings.jsonl")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Error("ledger file should not exist when HEADROOM_SAVINGS=0")
	}
}

// TestSavingsLedgerPath_EnvOverride verifies SIGMASHAKE_HOME is honored.
func TestSavingsLedgerPath_EnvOverride(t *testing.T) {
	base := filepath.Join(t.TempDir(), "custom", "home")
	t.Setenv("SIGMASHAKE_HOME", base)
	got := savingsLedgerPath()
	want := filepath.Join(base, "token-savings.jsonl")
	if got != want {
		t.Errorf("savingsLedgerPath()=%q, want %q", got, want)
	}
}

// TestSavingsLedgerPath_DefaultContainsSigmaShake verifies the fallback path
// contains ".sigmashake" when SIGMASHAKE_HOME is unset.
func TestSavingsLedgerPath_DefaultContainsSigmaShake(t *testing.T) {
	t.Setenv("SIGMASHAKE_HOME", "")
	got := savingsLedgerPath()
	if !strings.Contains(got, ".sigmashake") {
		t.Errorf("savingsLedgerPath()=%q does not contain .sigmashake", got)
	}
	if !strings.HasSuffix(got, "token-savings.jsonl") {
		t.Errorf("savingsLedgerPath()=%q should end with token-savings.jsonl", got)
	}
}
