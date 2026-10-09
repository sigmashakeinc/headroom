package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- test helpers ---

// isolateHook points the store + savings ledger at temp dirs and clears the
// disable switches so the hook is enabled by default.
func isolateHook(t *testing.T) (home, store string) {
	t.Helper()
	home = t.TempDir()
	store = t.TempDir()
	t.Setenv("SIGMASHAKE_HOME", home)  // token-savings.jsonl lives here
	t.Setenv("HEADROOM_HOME", store)   // gzip store root
	t.Setenv("HEADROOM_DISABLE", "")   // enabled
	t.Setenv("HEADROOM_AUTO", "")      // enabled
	t.Setenv("HEADROOM_AUTO_MIN", "")  // default floor
	t.Setenv("HEADROOM_SAVINGS", "")   // ledger on
	t.Setenv("HEADROOM_MAX_LINES", "") // default
	return home, store
}

func runHookCapture(t *testing.T, client, payload string) string {
	t.Helper()
	prev := stdinForHook
	stdinForHook = func() io.Reader { return strings.NewReader(payload) }
	defer func() { stdinForHook = prev }()

	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()

	if rc := cmdHook([]string{"--client=" + client}); rc != 0 {
		t.Errorf("cmdHook exit = %d, want 0", rc)
	}
	_ = w.Close()
	os.Stdout = old
	return <-done
}

func ledgerLines(t *testing.T, home string) int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(home, "token-savings.jsonl"))
	if err != nil {
		return 0
	}
	n := 0
	for _, ln := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(ln) != "" {
			n++
		}
	}
	return n
}

func bashPayload(command string, resp any) string {
	m := map[string]any{
		"tool_name":     "Bash",
		"tool_input":    map[string]any{"command": command},
		"tool_response": resp,
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func objResp(stdout string) map[string]any {
	return map[string]any{"stdout": stdout, "stderr": "", "interrupted": false, "isImage": false}
}

// bigDedup returns >floor bytes that compress well (repeated lines).
func bigDedup() string {
	return strings.Repeat("INFO 2026-07-01 request handled id=42 status=ok latency=3ms\n", 200)
}

// --- classification ---

func TestClassifyReadOnly(t *testing.T) {
	cases := []struct {
		cmd  string
		want bool
	}{
		{"cat big.json", true},
		{"cat /var/log/app.log", true},
		{"cat -n big.log", true},
		{"jq . data.json", true},
		{"jq -r .name data.json", true},
		// misses — ONLY cat/jq are read-only under all flags. kubectl/terraform/git
		// can invoke exec-cred plugins, launch provider executables, or write the
		// git index; curl/wget + git log/diff/show are unclassifiable by string
		// parsing — all fail SAFE to a no-op:
		{"kubectl get pods", false},
		{"kubectl describe pod x", false},
		{"terraform show", false},
		{"git status", false},
		{"git status --porcelain", false},
		{"git log", false},
		{"git log --oneline", false},
		{"curl https://example.com/api", false},
		{"curl -s https://example.com", false},
		{"wget -O - https://example.com", false},
		{"wget -qO- https://example.com", false},
		{"git diff", false},
		{"git diff HEAD", false},
		{"git show HEAD", false},
		{"kubectl delete pod x", false},
		{"kubectl apply -f x.yaml", false},
		{"terraform apply", false},
		{"curl -o out.bin https://x", false},
		{"curl -O https://x/file", false},
		{"curl -X POST https://x", false},
		{"curl -d name=v https://x", false},
		{"curl -sSLO https://x/file", false},
		{"wget https://example.com", false}, // writes a file by default
		{"wget -O out.html https://x", false},
		{"bun test", false},
		{"npm run build", false},
		{"go build ./...", false},
		// shell operators
		{"cat a | sh", false},
		{"cat a; echo two", false},
		{"git status && go test ./...", false},
		{"cat a > out.txt", false},
		{"echo $(cat secret)", false},
		{"cat `whoami`", false},
		// already wrapped / disable prefix
		{"headroom wrap cat a", false},
		{"stk git status", false},
		{"bun run gate go build", false},
		{"HEADROOM_DISABLE=1 cat big.json", false},
		{"HEADROOM_AUTO=0 cat big.json", false},
		// env prefixes are rejected outright — could hide a command-local disable
		// or a resolution/loader override (PATH/IFS/LD_PRELOAD) — even a benign one.
		{"TZ=UTC cat big.json", false},
		{"FOO=1 cat big.json", false},
		{"PATH=/tmp:$PATH cat big.json", false},
		// path executables never trusted (basename could mask an arbitrary binary)
		{"/tmp/cat big.json", false},
		{"./git status", false},
		{"/usr/bin/cat big.json", false},
		// env-assignment VALUE carrying shell operators is caught before stripping
		{"FOO=$(id) cat big.json", false},
		// curl that loads options from a file or writes local files
		{"curl -K post.conf https://x", false},
		{"curl --config post.conf https://x", false},
		{"curl -c cookies.txt https://x", false},
		{"curl -D headers.txt https://x", false},
		// wget to stdout but mutating HTTP (post/method)
		{"wget -qO- --post-data=x https://api", false},
		{"wget -qO- --method=POST https://api", false},
		{"", false},
	}
	for _, c := range cases {
		if got := classifyReadOnly(c.cmd); got != c.want {
			t.Errorf("classifyReadOnly(%q) = %v, want %v", c.cmd, got, c.want)
		}
	}
}

// --- end-to-end: claude ---

func TestCmdHook_ClaudeCompressesAndLogs(t *testing.T) {
	home, store := isolateHook(t)
	out := bigDedup()
	res := runHookCapture(t, "claude", bashPayload("cat big.log", objResp(out)))

	if res == "" {
		t.Fatal("expected replacement JSON, got empty (no-op)")
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(res)), &parsed); err != nil {
		t.Fatalf("output not JSON: %v\n%s", err, res)
	}
	hso, _ := parsed["hookSpecificOutput"].(map[string]any)
	if hso["hookEventName"] != "PostToolUse" {
		t.Errorf("hookEventName = %v", hso["hookEventName"])
	}
	uto, ok := hso["updatedToolOutput"].(map[string]any)
	if !ok {
		t.Fatalf("updatedToolOutput missing/typed wrong: %v", hso["updatedToolOutput"])
	}
	stdout, _ := uto["stdout"].(string)
	if !strings.Contains(stdout, "headroom get ") || !strings.Contains(stdout, "to expand") {
		t.Errorf("replacement missing retrieval footer: %q", stdout)
	}
	if len(stdout) >= len(out) {
		t.Errorf("replacement (%d) not smaller than original (%d)", len(stdout), len(out))
	}
	// Ledger got exactly one honest entry.
	if n := ledgerLines(t, home); n != 1 {
		t.Fatalf("ledger lines = %d, want 1", n)
	}
	// Original is byte-retrievable from the store.
	handle := extractHandle(t, stdout)
	got, err := NewStore(store).Get(handle)
	if err != nil {
		t.Fatalf("store.Get(%q): %v", handle, err)
	}
	if string(got) != out {
		t.Errorf("retrieved original mismatch: got %d bytes, want %d", len(got), len(out))
	}
}

func TestCmdHook_CodexBlockDecisionWithNotAFailurePrefix(t *testing.T) {
	isolateHook(t)
	res := runHookCapture(t, "codex", bashPayload("cat big.log", objResp(bigDedup())))
	if res == "" {
		t.Fatal("expected codex replacement, got no-op")
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(res)), &parsed); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if parsed["decision"] != "block" {
		t.Errorf("decision = %v, want block", parsed["decision"])
	}
	reason, _ := parsed["reason"].(string)
	if !strings.HasPrefix(reason, "Output compressed by headroom") || !strings.Contains(reason, "not a failure") {
		t.Errorf("codex reason missing not-a-failure prefix: %q", reason)
	}
}

// --- string-shaped tool_response ---

func TestCmdHook_ClaudeStringResponse(t *testing.T) {
	isolateHook(t)
	res := runHookCapture(t, "claude", bashPayload("cat big.log", bigDedup()))
	var parsed map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(res)), &parsed); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	hso := parsed["hookSpecificOutput"].(map[string]any)
	if _, isString := hso["updatedToolOutput"].(string); !isString {
		t.Errorf("string tool_response should yield string updatedToolOutput, got %T", hso["updatedToolOutput"])
	}
}

// --- no-op cases ---

func TestCmdHook_NoOpCases(t *testing.T) {
	cases := []struct {
		name    string
		client  string
		command string
		resp    any
		env     map[string]string
	}{
		{"below floor", "claude", "cat small.txt", objResp("tiny"), nil},
		{"allowlist miss git diff", "claude", "git diff", objResp(bigDedup()), nil},
		{"shell operator", "claude", "cat a | sh", objResp(bigDedup()), nil},
		{"interrupted", "claude", "cat big.log", map[string]any{"stdout": bigDedup(), "interrupted": true}, nil},
		{"isImage", "claude", "cat img", map[string]any{"stdout": bigDedup(), "isImage": true}, nil},
		{"errored exit", "claude", "cat big.log", map[string]any{"stdout": bigDedup(), "is_error": true}, nil},
		{"nonzero exit code", "claude", "cat big.log", map[string]any{"stdout": bigDedup(), "exit_code": 2}, nil},
		{"disable env", "claude", "cat big.log", objResp(bigDedup()), map[string]string{"HEADROOM_DISABLE": "1"}},
		{"auto off env", "claude", "cat big.log", objResp(bigDedup()), map[string]string{"HEADROOM_AUTO": "0"}},
		{"command-local disable", "claude", "HEADROOM_DISABLE=1 cat big.log", objResp(bigDedup()), nil},
		{"binary output", "claude", "cat blob", objResp("head\x00" + bigDedup()), nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			home, _ := isolateHook(t)
			for k, v := range c.env {
				t.Setenv(k, v)
			}
			res := runHookCapture(t, c.client, bashPayload(c.command, c.resp))
			if strings.TrimSpace(res) != "" {
				t.Errorf("expected no-op, got output: %s", res)
			}
			if n := ledgerLines(t, home); n != 0 {
				t.Errorf("expected 0 ledger lines on no-op, got %d", n)
			}
		})
	}
}

func TestCmdHook_MalformedAndNonBash(t *testing.T) {
	isolateHook(t)
	for _, p := range []string{"", "  ", "not json", `{"tool_name":"Read","tool_input":{"file_path":"x"}}`,
		`{"tool_name":"Bash","tool_input":{}}`, `{"tool_name":"Bash"}`} {
		if res := runHookCapture(t, "claude", p); strings.TrimSpace(res) != "" {
			t.Errorf("payload %q: expected no-op, got %s", p, res)
		}
	}
}

func TestCmdHook_UnknownClientNoOp(t *testing.T) {
	isolateHook(t)
	prev := stdinForHook
	stdinForHook = func() io.Reader { return strings.NewReader(bashPayload("cat big.log", objResp(bigDedup()))) }
	defer func() { stdinForHook = prev }()
	// no --client flag => no-op, no panic
	if rc := cmdHook(nil); rc != 0 {
		t.Errorf("rc = %d", rc)
	}
}

// --- fail-open on store error ---

func TestCmdHook_FailOpenOnStoreError(t *testing.T) {
	home := t.TempDir()
	t.Setenv("SIGMASHAKE_HOME", home)
	t.Setenv("HEADROOM_DISABLE", "")
	t.Setenv("HEADROOM_AUTO", "")
	// Point the store root at a regular FILE so Put's mkdir fails.
	badFile := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(badFile, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HEADROOM_HOME", badFile)

	res := runHookCapture(t, "claude", bashPayload("cat big.log", objResp(bigDedup())))
	if strings.TrimSpace(res) != "" {
		t.Errorf("store failure must fail-open (no output), got: %s", res)
	}
	if n := ledgerLines(t, home); n != 0 {
		t.Errorf("no savings should be logged when store fails, got %d", n)
	}
}

// --- would-save guard: output that does not shrink after digest+footer ---

func TestCmdHook_WouldSaveGuard(t *testing.T) {
	home, _ := isolateHook(t)
	// One long single line (no dedup/truncation opportunity) just over the floor:
	// digest ≈ original, and +footer pushes finalText >= original => no-op.
	line := strings.Repeat("x", 2100)
	res := runHookCapture(t, "claude", bashPayload("cat one.line", objResp(line)))
	if strings.TrimSpace(res) != "" {
		t.Errorf("would-save guard should no-op, got: %s", res)
	}
	if n := ledgerLines(t, home); n != 0 {
		t.Errorf("would-save no-op must not log, got %d", n)
	}
}

func extractHandle(t *testing.T, s string) string {
	t.Helper()
	i := strings.Index(s, "[headroom:")
	if i < 0 {
		t.Fatalf("no footer in %q", s)
	}
	rest := s[i+len("[headroom:"):]
	j := strings.IndexByte(rest, ' ')
	if j < 0 {
		t.Fatalf("malformed footer in %q", s)
	}
	return rest[:j]
}
