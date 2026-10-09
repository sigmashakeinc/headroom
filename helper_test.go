package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// TestMain lets the test binary act as a portable child process for wrap
// tests. When HEADROOM_TEST_CHILD is set, the binary does not run tests.
//
//	exit:N  exits with code N.
//	print   writes HEADROOM_TEST_TEXT to stdout and exits 0.
func TestMain(m *testing.M) {
	if mode := os.Getenv("HEADROOM_TEST_CHILD"); mode != "" {
		switch {
		case strings.HasPrefix(mode, "exit:"):
			n, _ := strconv.Atoi(strings.TrimPrefix(mode, "exit:"))
			os.Exit(n)
		case mode == "print":
			fmt.Print(os.Getenv("HEADROOM_TEST_TEXT"))
			os.Exit(0)
		}
		os.Exit(99)
	}
	os.Exit(m.Run())
}

// permsEnforced reports whether file-mode denial is enforced for this process.
// It is false on Windows and for root.
func permsEnforced() bool {
	return runtime.GOOS != "windows" && os.Getuid() != 0
}

func TestVersionDefaultsToDev(t *testing.T) {
	if version == "" {
		t.Fatal("version must not be empty")
	}
	out := captureStdout(t, func() {
		if rc := run([]string{"--version"}); rc != 0 {
			t.Errorf("--version rc=%d", rc)
		}
	})
	if want := "headroom " + version + "\n"; out != want {
		t.Errorf("--version output %q, want %q", out, want)
	}
}

func TestStoreRoot_HeadroomHomeOverrideUsedByPut(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "custom-store")
	t.Setenv("HEADROOM_HOME", dir)
	if got := defaultStoreRoot(); got != dir {
		t.Fatalf("defaultStoreRoot()=%q want %q", got, dir)
	}
	st := NewStore(defaultStoreRoot())
	h, err := st.Put([]byte("payload"), "text")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, h+blobExt)); err != nil {
		t.Errorf("blob not under HEADROOM_HOME: %v", err)
	}
}

func TestStoreRoot_DefaultUnderUserHome(t *testing.T) {
	t.Setenv("HEADROOM_HOME", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	want := filepath.Join(home, ".sigmashake", "headroom")
	if got := defaultStoreRoot(); got != want {
		t.Errorf("defaultStoreRoot()=%q want %q", got, want)
	}
}

func TestLedgerPath_DefaultUnderUserHome(t *testing.T) {
	t.Setenv("SIGMASHAKE_HOME", "")
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	want := filepath.Join(home, ".sigmashake", "token-savings.jsonl")
	if got := savingsLedgerPath(); got != want {
		t.Errorf("savingsLedgerPath()=%q want %q", got, want)
	}
}
