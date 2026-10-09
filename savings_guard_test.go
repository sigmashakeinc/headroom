package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAppendSavings_NoiseGuard verifies the guard that keeps non-savings and
// non-positive-original events out of the shared ledger (the historical source of
// the "-0%" rows that made the tokenomics dashboard read "headroom saves nothing").
func TestAppendSavings_NoiseGuard(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SIGMASHAKE_HOME", home)
	t.Setenv("HEADROOM_SAVINGS", "")

	appendSavings(0, 5)     // non-positive original
	appendSavings(100, 100) // no savings
	appendSavings(100, 200) // negative savings
	if n := ledgerLines(t, home); n != 0 {
		t.Fatalf("noise entries should be dropped, got %d", n)
	}

	appendSavings(100, 40) // a genuine reduction
	if n := ledgerLines(t, home); n != 1 {
		t.Fatalf("real saving should log exactly 1, got %d", n)
	}
}

// TestRotateLedger_KeepsRecentUnderCap verifies the append-only ledger is bounded:
// once it exceeds ledgerMaxBytes, the next append rotates it to the most recent
// ledgerKeepLines entries (plus the new one), preserving the fresh event.
func TestRotateLedger_KeepsRecentUnderCap(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SIGMASHAKE_HOME", home)
	t.Setenv("HEADROOM_SAVINGS", "")
	path := filepath.Join(home, "token-savings.jsonl")

	var b strings.Builder
	line := `{"tool":"headroom","ts":1,"orig_bytes":1000,"comp_bytes":200}` + "\n"
	for b.Len() <= ledgerMaxBytes+len(line) {
		b.WriteString(line)
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	if before := ledgerLines(t, home); before <= ledgerKeepLines {
		t.Fatalf("setup: want > %d lines, got %d", ledgerKeepLines, before)
	}

	appendSavings(1000, 100) // triggers rotation

	if after := ledgerLines(t, home); after > ledgerKeepLines+1 {
		t.Errorf("after rotation lines = %d, want <= %d", after, ledgerKeepLines+1)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), `"comp_bytes":100`) {
		t.Error("freshly appended entry missing after rotation")
	}
}
