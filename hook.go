package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
)

// autoMinBytesDefault is the default minimum combined-output size (bytes) below
// which the PostToolUse auto-compression hook is a no-op: tiny outputs aren't
// worth compressing and would only add ledger noise. Override via HEADROOM_AUTO_MIN.
const autoMinBytesDefault = 2048

// hookStdinLimit caps how much of the PostToolUse payload we read (defensive).
const hookStdinLimit = 16 << 20 // 16 MiB

// cmdHook implements `headroom hook --client=claude|codex`: the operator-sanctioned
// (2026-07-01) PostToolUse OUTPUT-compression handler. It reads a PostToolUse hook
// JSON object from stdin and, for a SIMPLE read-only allowlisted Bash command whose
// output exceeds the size floor, compresses the RESULT (the command already ran,
// unmodified), stores the original byte-for-byte, logs the real savings, and prints
// the harness-specific output-replacement JSON. Any miss, disable, store failure, or
// non-saving outcome => no-op (emit nothing, exit 0).
//
// This NEVER rewrites the command that runs — it is not the rtk PreToolUse footgun.
// The command executes exactly as typed; only its already-produced output is replaced
// with a shorter, byte-retrievable digest (`headroom get <handle>`).
func cmdHook(args []string) int {
	client := ""
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--client" && i+1 < len(args):
			client = args[i+1]
			i++
		case strings.HasPrefix(a, "--client="):
			client = strings.TrimPrefix(a, "--client=")
		}
	}
	// Unknown/missing client => passthrough no-op (never break the tool call).
	if client != "claude" && client != "codex" {
		return 0
	}

	// Global kill-switch (process env).
	if os.Getenv("HEADROOM_DISABLE") == "1" || isFalsey(os.Getenv("HEADROOM_AUTO")) {
		return 0
	}

	raw, _ := io.ReadAll(io.LimitReader(stdinForHook(), hookStdinLimit))
	if len(bytes.TrimSpace(raw)) == 0 {
		return 0
	}
	var parsed map[string]json.RawMessage
	if json.Unmarshal(raw, &parsed) != nil {
		return 0
	}

	if hookStr(parsed, "tool_name", "toolName") != "Bash" {
		return 0
	}

	command := ""
	if ti := hookRaw(parsed, "tool_input", "toolInput"); ti != nil {
		var input map[string]json.RawMessage
		if json.Unmarshal(ti, &input) == nil {
			command = hookStr(input, "command")
		}
	}
	if strings.TrimSpace(command) == "" {
		return 0
	}

	stdout, stderr, interrupted, isImage, isError, wasString := parseToolResponse(parsed)
	if interrupted || isImage || isError {
		return 0 // agents need interrupted/binary/failed output verbatim
	}

	if !classifyReadOnly(command) {
		return 0
	}

	combined := stdout
	if stderr != "" {
		if combined != "" && !strings.HasSuffix(combined, "\n") {
			combined += "\n"
		}
		combined += stderr
	}
	if combined == "" || strings.IndexByte(combined, 0) >= 0 {
		return 0 // empty or binary
	}
	if len(combined) <= autoMinBytes() {
		return 0
	}

	original := []byte(combined)
	compressed, kind := Compress(original, "auto", resolveMaxLines(0))

	// Store the ORIGINAL before emitting any lossy digest. Fail-open: if we cannot
	// losslessly retain the original, leave the real output untouched.
	st := NewStore(defaultStoreRoot())
	handle, err := st.Put(original, kind)
	if err != nil {
		return 0
	}

	prefix := ""
	if client == "codex" {
		// `decision:"block"` replaces the Codex tool result with `reason`; the word
		// "block" is semantically loaded, so state plainly this is not a failure.
		prefix = "Output compressed by headroom (command already ran successfully; this is not a failure).\n"
	}
	digest := string(compressed)
	if !strings.HasSuffix(digest, "\n") {
		digest += "\n"
	}
	core := prefix + digest
	// Compute the displayed pct against the FINAL replacement length (core + footer),
	// not just the digest, so extreme compression doesn't round up to a misleading
	// -100%. Two-pass: the pct digits barely change the footer length. The ledger
	// below records the exact model-visible bytes regardless.
	footerFmt := "[headroom:%s · -%d%% · headroom get %s to expand — cached ~24h; retrieve before line-exact edits]"
	pct := savingsPct(len(original), len(core)+len(fmt.Sprintf(footerFmt, handle, 0, handle)))
	finalText := core + fmt.Sprintf(footerFmt, handle, pct, handle)

	// Would-save guard: never replace real output with something no smaller.
	if len(finalText) >= len(original) {
		return 0
	}

	// Honest accounting: comp = the actual bytes the model will now see.
	appendSavings(len(original), len(finalText))

	switch client {
	case "claude":
		emitHookJSON(map[string]any{
			"hookSpecificOutput": map[string]any{
				"hookEventName":     "PostToolUse",
				"updatedToolOutput": claudeUpdatedOutput(finalText, wasString),
			},
		})
	case "codex":
		emitHookJSON(map[string]any{
			"hookSpecificOutput": map[string]any{"hookEventName": "PostToolUse"},
			"decision":           "block",
			"reason":             finalText,
		})
	}
	return 0
}

// stdinForHook indirection lets tests inject a payload without a real pipe.
var stdinForHook = func() io.Reader { return os.Stdin }

// claudeUpdatedOutput builds the value for Claude's `updatedToolOutput`, matching
// the shape of the original tool_response so Claude does not silently fall back to
// the original: an object for the Bash `{stdout,stderr,interrupted,isImage}` shape,
// or a bare string when the original response was a string.
func claudeUpdatedOutput(finalText string, wasString bool) any {
	if wasString {
		return finalText
	}
	return map[string]any{
		"stdout":      finalText,
		"stderr":      "",
		"interrupted": false,
		"isImage":     false,
	}
}

// parseToolResponse extracts the fields we need from tool_response, which may be a
// JSON object (Bash: stdout/stderr/interrupted/isImage) or a plain string.
func parseToolResponse(parsed map[string]json.RawMessage) (stdout, stderr string, interrupted, isImage, isError, wasString bool) {
	raw := hookRaw(parsed, "tool_response", "toolResponse")
	if raw == nil || string(bytes.TrimSpace(raw)) == "null" {
		return
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s, "", false, false, false, true
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil {
		return
	}
	stdout = hookStr(obj, "stdout", "output", "aggregated_output")
	stderr = hookStr(obj, "stderr")
	interrupted = hookBool(obj, "interrupted")
	isImage = hookBool(obj, "isImage", "is_image")
	if hookBool(obj, "is_error", "isError") {
		isError = true
	}
	if code, ok := hookInt(obj, "returncode", "exit_code", "exitCode", "code"); ok && code != 0 {
		isError = true
	}
	return
}

// classifyReadOnly reports whether command is a SIMPLE, single, read-only allowlisted
// invocation eligible for auto-compression. It strips leading KEY=value env prefixes
// (honoring a command-local HEADROOM_DISABLE/HEADROOM_AUTO opt-out), rejects any shell
// control operator (so chained/piped/redirected/substituted commands whose final
// output is NOT the allowlisted read-only output are never touched), and enforces the
// per-verb constraints.
func classifyReadOnly(command string) bool {
	cmd := strings.TrimSpace(command)
	if cmd == "" {
		return false
	}

	// Reject any shell control / substitution / redirection / expansion on the
	// ORIGINAL string (before any tokenizing) so operators hidden inside an env
	// assignment value — e.g. `FOO=$(>/tmp/x) cat big.log` — cannot slip through.
	// Only a single simple command whose output IS the allowlisted read-only
	// output is eligible; `$` covers `$(...)` and `${...}`.
	if strings.ContainsAny(cmd, ";&|<>$()`\n") {
		return false
	}
	if strings.HasPrefix(cmd, "headroom ") || strings.HasPrefix(cmd, "stk ") || strings.HasPrefix(cmd, "bun run gate ") {
		return false
	}

	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return false
	}
	// Reject any leading VAR=value env prefix outright: it may be a command-local
	// opt-out (HEADROOM_DISABLE/HEADROOM_AUTO) OR a command-resolution/loader
	// override that changes what actually runs (PATH=/tmp:$PATH, IFS=, LD_PRELOAD=,
	// BASH_ENV=). Either way this is not a plain read-only command => no-op.
	if isEnvAssign(fields[0]) {
		return false
	}
	// Only bare command names — never a path executable (/tmp/cat, ./git) whose
	// argv0 basename merely *looks* allowlisted while running an arbitrary binary.
	verb := fields[0]
	if strings.ContainsAny(verb, `/\`) {
		return false
	}
	// Only `cat` and `jq` are read-only under EVERY flag: neither has any flag that
	// writes a file, mutates state, makes a network call, or executes an external
	// program, so no smuggled/quoted/brace-expanded flag can turn them into a writer.
	// Everything else is excluded and fails SAFE (no-op): curl/wget (an unbounded
	// file-writing + `--config`/`--libcurl`/`--write-out` surface that is not safely
	// classifiable by string-parsing); git log/diff/show (exact evidence + `--ext-diff`
	// external-command execution); and even kubectl get / terraform show / git status,
	// which can invoke exec-credential plugins, launch provider executables, or refresh
	// and WRITE the git index. Those can be re-added later behind a real shell-argv
	// parser + an exec/write-aware safe classifier.
	return verb == "cat" || verb == "jq"
}

// --- small helpers ---

func autoMinBytes() int {
	if v := os.Getenv("HEADROOM_AUTO_MIN"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			return n
		}
	}
	return autoMinBytesDefault
}

func isFalsey(s string) bool { return s == "0" || s == "false" }

func emitHookJSON(v map[string]any) {
	if b, err := json.Marshal(v); err == nil {
		fmt.Fprintln(os.Stdout, string(b))
	}
}

// hookStr returns the first present string field among keys.
func hookStr(m map[string]json.RawMessage, keys ...string) string {
	for _, k := range keys {
		if raw, ok := m[k]; ok {
			var s string
			if json.Unmarshal(raw, &s) == nil {
				return s
			}
		}
	}
	return ""
}

func hookRaw(m map[string]json.RawMessage, keys ...string) json.RawMessage {
	for _, k := range keys {
		if raw, ok := m[k]; ok {
			return raw
		}
	}
	return nil
}

func hookBool(m map[string]json.RawMessage, keys ...string) bool {
	for _, k := range keys {
		if raw, ok := m[k]; ok {
			var b bool
			if json.Unmarshal(raw, &b) == nil {
				return b
			}
		}
	}
	return false
}

func hookInt(m map[string]json.RawMessage, keys ...string) (int, bool) {
	for _, k := range keys {
		if raw, ok := m[k]; ok {
			var n float64
			if json.Unmarshal(raw, &n) == nil {
				return int(n), true
			}
		}
	}
	return 0, false
}

// isEnvAssign reports whether tok is a shell KEY=value environment assignment.
func isEnvAssign(tok string) bool {
	eq := strings.IndexByte(tok, '=')
	if eq <= 0 {
		return false
	}
	for i, c := range tok[:eq] {
		if c == '_' || (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (i > 0 && c >= '0' && c <= '9') {
			continue
		}
		return false
	}
	return true
}
