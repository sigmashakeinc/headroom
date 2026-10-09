package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

// captureStdout runs fn with os.Stdout redirected and returns what it wrote.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		var b bytes.Buffer
		_, _ = io.Copy(&b, r)
		done <- b.String()
	}()
	fn()
	_ = w.Close()
	os.Stdout = old
	return <-done
}

func TestRunDispatch(t *testing.T) {
	if rc := run(nil); rc != 1 {
		t.Errorf("empty args rc=%d want 1", rc)
	}
	for _, a := range []string{"--version", "version"} {
		out := captureStdout(t, func() {
			if rc := run([]string{a}); rc != 0 {
				t.Errorf("%s rc!=0", a)
			}
		})
		if !strings.Contains(out, "headroom") {
			t.Errorf("%s out=%q", a, out)
		}
	}
	for _, a := range []string{"--help", "-h", "help"} {
		out := captureStdout(t, func() {
			if rc := run([]string{a}); rc != 0 {
				t.Errorf("%s rc!=0", a)
			}
		})
		if !strings.Contains(out, "Usage") {
			t.Errorf("%s missing usage", a)
		}
	}
	if rc := run([]string{"bogus-subcommand"}); rc != 1 {
		t.Errorf("unknown rc=%d want 1", rc)
	}
}

func TestCmdPerfAndGet(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HEADROOM_HOME", dir)
	t.Setenv("SIGMASHAKE_HOME", dir)

	if out := captureStdout(t, func() { cmdPerf() }); !strings.Contains(out, "empty") {
		t.Errorf("perf on empty store: %q", out)
	}

	original := []byte("INFO x\nINFO x\nINFO x\nDone\n")
	st := NewStore(defaultStoreRoot())
	handle, err := st.Put(original, "text")
	if err != nil {
		t.Fatal(err)
	}
	if out := captureStdout(t, func() { cmdPerf() }); !strings.Contains(out, "entries:") {
		t.Errorf("perf on non-empty store: %q", out)
	}

	got := captureStdout(t, func() {
		if rc := cmdGet([]string{handle}); rc != 0 {
			t.Errorf("get rc!=0")
		}
	})
	if got != string(original) {
		t.Errorf("get mismatch: %q", got)
	}
	if rc := cmdGet([]string{"deadbeef00"}); rc != 1 {
		t.Errorf("get unknown handle rc=%d want 1", rc)
	}
	if rc := cmdGet(nil); rc != 1 {
		t.Errorf("get no-arg rc=%d want 1", rc)
	}
}

func TestWrapExitAndRawAndEmpty(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HEADROOM_HOME", dir)
	t.Setenv("SIGMASHAKE_HOME", dir)

	// Non-zero child exit propagates exactly (exercises cmdExitCode).
	t.Setenv("HEADROOM_TEST_CHILD", "exit:3")
	captureStdout(t, func() {
		if rc := run([]string{"wrap", os.Args[0]}); rc != 3 {
			t.Errorf("wrap propagated rc=%d want 3", rc)
		}
	})
	// --raw passthrough path.
	t.Setenv("HEADROOM_TEST_CHILD", "exit:0")
	captureStdout(t, func() {
		if rc := run([]string{"wrap", "--raw", os.Args[0]}); rc != 0 {
			t.Errorf("raw rc!=0")
		}
	})
	// wrap with no command => usage error.
	if rc := run([]string{"wrap"}); rc != 1 {
		t.Errorf("wrap empty rc=%d want 1", rc)
	}
}

func TestPureHelpers(t *testing.T) {
	if humanBytes(500) != "500 B" {
		t.Errorf("humanBytes(500)=%q", humanBytes(500))
	}
	if humanBytes(2048) != "2.0 KiB" {
		t.Errorf("humanBytes(2048)=%q", humanBytes(2048))
	}
	if humanBytes(3<<20) != "3.0 MiB" {
		t.Errorf("humanBytes(3MiB)=%q", humanBytes(3<<20))
	}
	if savingsPct(1000, 200) != 80 {
		t.Error("savingsPct(1000,200)!=80")
	}
	if savingsPct(0, 5) != 0 || savingsPct(100, 200) != 0 {
		t.Error("savingsPct clamp")
	}

	t.Setenv("HEADROOM_MAX_LINES", "123")
	if resolveMaxLines(0) != 123 {
		t.Error("resolveMaxLines env")
	}
	if resolveMaxLines(50) != 50 {
		t.Error("resolveMaxLines explicit")
	}
	t.Setenv("HEADROOM_MAX_LINES", "")
	if resolveMaxLines(0) != DefaultMaxLines {
		t.Error("resolveMaxLines default")
	}

	t.Setenv("HEADROOM_AUTO_MIN", "100")
	if autoMinBytes() != 100 {
		t.Error("autoMinBytes env")
	}
	t.Setenv("HEADROOM_AUTO_MIN", "")
	if autoMinBytes() != autoMinBytesDefault {
		t.Error("autoMinBytes default")
	}
}
