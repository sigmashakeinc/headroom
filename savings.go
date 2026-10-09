package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// ledgerMaxBytes caps the token-savings ledger; past it the file is rotated in
// place, keeping only the most recent ledgerKeepLines entries. Unlike the gzip
// store (which self-prunes), the append-only ledger has no other bound, and the
// default-ON PostToolUse hook can append on every large read-only command.
const (
	ledgerMaxBytes  = 5 << 20 // 5 MiB
	ledgerKeepLines = 5000
)

// rotateLedgerIfNeeded truncates the ledger to its last ledgerKeepLines lines when
// it exceeds ledgerMaxBytes. Best-effort: any error leaves the ledger untouched and
// never fails the caller.
func rotateLedgerIfNeeded(path string) {
	fi, err := os.Stat(path)
	if err != nil || fi.Size() <= ledgerMaxBytes {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	lines := bytes.Split(data, []byte("\n"))
	kept := make([][]byte, 0, ledgerKeepLines)
	for _, ln := range lines {
		if len(bytes.TrimSpace(ln)) > 0 {
			kept = append(kept, ln)
		}
	}
	if len(kept) > ledgerKeepLines {
		kept = kept[len(kept)-ledgerKeepLines:]
	}
	out := bytes.Join(kept, []byte("\n"))
	if len(out) > 0 {
		out = append(out, '\n')
	}
	tmp := path + ".tmp"
	if os.WriteFile(tmp, out, 0o644) != nil {
		return
	}
	_ = os.Rename(tmp, path)
}

// savingsLedgerPath returns the path to the shared append-only token-savings
// ledger. Honors SIGMASHAKE_HOME env; falls back to
// os.UserHomeDir()+"/.sigmashake".
func savingsLedgerPath() string {
	if base := os.Getenv("SIGMASHAKE_HOME"); base != "" {
		return filepath.Join(base, "token-savings.jsonl")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".sigmashake", "token-savings.jsonl")
	}
	return filepath.Join(home, ".sigmashake", "token-savings.jsonl")
}

// appendSavings appends one reduction event to the shared token-savings ledger.
//
//   - origBytes: byte length of the original input content.
//   - compBytes: byte length of the compressed DIGEST emitted to stdout (the
//     text that reaches the LLM context) — NOT the gzip blob stored for
//     retrieval.
//
// Best-effort: any I/O error is silently discarded — must not alter the digest
// output, the exit code, or fail the command.
// Skipped when HEADROOM_SAVINGS is "0" or "false".
func appendSavings(origBytes, compBytes int) {
	switch os.Getenv("HEADROOM_SAVINGS") {
	case "0", "false":
		return
	}
	// Noise guard: only record genuine reductions. Non-positive originals and
	// non-savings (compressed >= original) would otherwise pollute the shared
	// ledger with "-0%"/negative entries (the historical source of the empty
	// test rows the tokenomics dashboard surfaced as "headroom saves nothing").
	if origBytes <= 0 || compBytes >= origBytes {
		return
	}
	path := savingsLedgerPath()
	_ = os.MkdirAll(filepath.Dir(path), 0o755) // best-effort; ignore error
	rotateLedgerIfNeeded(path)                 // bound unbounded growth under the per-output hook
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return // non-fatal
	}
	defer f.Close()
	line := fmt.Sprintf(
		`{"tool":"headroom","ts":%d,"orig_bytes":%d,"comp_bytes":%d}`+"\n",
		time.Now().Unix(), origBytes, compBytes,
	)
	_, _ = f.Write([]byte(line))
}
