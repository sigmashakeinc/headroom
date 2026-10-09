package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
)

// version is the release version. Release builds set it with
// -ldflags "-X main.version=<tag>". Default is "dev".
var version = "dev"

const usageText = `headroom — context-compression layer for AI agents.

Usage:
  headroom wrap [--raw] <cmd> [args...]
  headroom compress [--type auto|json|code|log|text] [--max N] [--no-store] [<file>|-]
  headroom get <handle>
  headroom perf
  headroom hook --client=claude|codex
  headroom --version | version

Commands:
  wrap       Run a command, capture combined output, compress it, store the
             original. Prints compressed digest + retrieval footer. Exit code
             of the wrapped command is propagated exactly.
             --raw or HEADROOM_DISABLE=1 => pure passthrough, no compression.
  compress   Compress a file or stdin (-). Prints digest + retrieval footer.
             --type  content type: auto|json|code|log|text (default: auto)
             --max N max lines for log/text truncation (default 500)
             --no-store  stateless: no original saved, no handle footer.
  get        Print the stored original for <handle> to stdout, byte-identical.
             Unknown handle => stderr message + exit 1.
  perf       Print total original bytes, compressed bytes, savings %, entry count.
  hook       PostToolUse auto-compression handler (operator-sanctioned, default-ON).
             Reads a PostToolUse hook JSON on stdin; for a SIMPLE read-only allowlisted
             Bash command (cat · jq — the only verbs that are read-only under every
             flag) over the size floor, replaces the tool RESULT with a compressed,
             byte-retrievable digest — the command is never rewritten. (curl/wget, git
             log/diff/show, kubectl/terraform/git status excluded: write-flag /
             exec-cred / provider / index-write surfaces.) Wired via ~/.claude/settings.json and
             .codex/hooks.json. No-op / fail-open on any miss. Disable with
             HEADROOM_DISABLE=1 or HEADROOM_AUTO=0.

Env:
  HEADROOM_DISABLE=1    Wrap passthrough (same as --raw); also disables the hook.
  HEADROOM_AUTO=0       Disable the PostToolUse auto-compression hook only.
  HEADROOM_AUTO_MIN=N   Min combined output bytes to auto-compress (default 2048).
  HEADROOM_MAX_LINES=N  Default max lines; --max N overrides.
  HEADROOM_HOME=<path>  Override store root (default ~/.sigmashake/headroom/).
  SIGMASHAKE_HOME=<path> Override the savings ledger dir (default ~/.sigmashake/).
  HEADROOM_SAVINGS=0    Do not write the savings ledger.

headroom never rewrites the command that runs; the hook handler compresses a
command's OUTPUT only (not the rtk command-rewrite footgun).
`

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		fmt.Print(usageText)
		return 1
	}
	switch args[0] {
	case "--version", "version":
		fmt.Printf("headroom %s\n", version)
		return 0
	case "--help", "-h", "help":
		fmt.Print(usageText)
		return 0
	case "wrap":
		return cmdWrap(args[1:])
	case "compress":
		return cmdCompress(args[1:])
	case "get":
		return cmdGet(args[1:])
	case "perf":
		return cmdPerf()
	case "hook":
		return cmdHook(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "headroom: unknown command %q\n", args[0])
		fmt.Print(usageText)
		return 1
	}
}

// cmdWrap runs a command, captures combined output, compresses it, stores the
// original, and prints the compressed digest + retrieval footer.
func cmdWrap(args []string) int {
	rawMode := os.Getenv("HEADROOM_DISABLE") == "1"

	i := 0
	for i < len(args) {
		switch args[i] {
		case "--raw":
			rawMode = true
			i++
		default:
			goto doneWrapFlags
		}
	}
doneWrapFlags:

	if i >= len(args) {
		fmt.Fprintf(os.Stderr, "headroom wrap: requires a command\n")
		return 1
	}

	cmdName := args[i]
	cmdArgs := args[i+1:]
	cmd := exec.Command(cmdName, cmdArgs...)

	if rawMode {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		cmd.Stdin = os.Stdin
		if err := cmd.Run(); err != nil {
			return cmdExitCode(err)
		}
		return 0
	}

	// Capture combined stdout+stderr.
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	cmd.Stdin = os.Stdin
	runErr := cmd.Run()

	original := buf.Bytes()
	compressed, kind := Compress(original, "auto", resolveMaxLines(0))
	appendSavings(len(original), len(compressed))

	// Store the original before printing (lossless reversibility invariant).
	st := NewStore(defaultStoreRoot())
	handle, storeErr := st.Put(original, kind)

	fmt.Print(string(compressed))
	if len(compressed) > 0 && compressed[len(compressed)-1] != '\n' {
		fmt.Println()
	}
	if storeErr == nil {
		pct := savingsPct(len(original), len(compressed))
		fmt.Fprintf(os.Stdout, "[headroom:%s · -%d%% · headroom get %s to expand]\n", handle, pct, handle)
	}

	if runErr != nil {
		return cmdExitCode(runErr)
	}
	return 0
}

// cmdCompress compresses a file or stdin and prints the digest + footer.
func cmdCompress(args []string) int {
	kind := "auto"
	maxLines := 0
	noStore := false
	fileArg := ""

	i := 0
	for i < len(args) {
		switch args[i] {
		case "--type":
			if i+1 >= len(args) {
				fmt.Fprintf(os.Stderr, "headroom compress: --type requires a value\n")
				return 1
			}
			kind = args[i+1]
			i += 2
		case "--max":
			if i+1 >= len(args) {
				fmt.Fprintf(os.Stderr, "headroom compress: --max requires a value\n")
				return 1
			}
			n, err := strconv.Atoi(args[i+1])
			if err != nil || n <= 0 {
				fmt.Fprintf(os.Stderr, "headroom compress: --max must be a positive integer, got %q\n", args[i+1])
				return 1
			}
			maxLines = n
			i += 2
		case "--no-store":
			noStore = true
			i++
		default:
			if fileArg == "" {
				fileArg = args[i]
			}
			i++
		}
	}

	// Validate --type.
	switch kind {
	case "auto", "json", "code", "log", "text":
	default:
		fmt.Fprintf(os.Stderr, "headroom compress: unknown --type %q (use auto|json|code|log|text)\n", kind)
		return 1
	}

	var input []byte
	var err error
	if fileArg == "" || fileArg == "-" {
		input, err = io.ReadAll(os.Stdin)
	} else {
		input, err = os.ReadFile(fileArg)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "headroom compress: %v\n", err)
		return 1
	}

	compressed, detectedKind := Compress(input, kind, resolveMaxLines(maxLines))
	appendSavings(len(input), len(compressed))

	fmt.Print(string(compressed))
	if len(compressed) > 0 && compressed[len(compressed)-1] != '\n' {
		fmt.Println()
	}
	if noStore {
		return 0
	}

	st := NewStore(defaultStoreRoot())
	handle, err := st.Put(input, detectedKind)
	if err != nil {
		fmt.Fprintf(os.Stderr, "headroom: store error: %v\n", err)
		return 1
	}
	pct := savingsPct(len(input), len(compressed))
	fmt.Fprintf(os.Stdout, "[headroom:%s · -%d%% · headroom get %s to expand]\n", handle, pct, handle)
	return 0
}

// cmdGet retrieves and prints a stored original, byte-identical.
func cmdGet(args []string) int {
	if len(args) == 0 {
		fmt.Fprintf(os.Stderr, "headroom get: requires a handle\n")
		return 1
	}
	handle := args[0]
	st := NewStore(defaultStoreRoot())
	data, err := st.Get(handle)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	os.Stdout.Write(data)
	return 0
}

// cmdPerf prints aggregate store statistics.
func cmdPerf() int {
	st := NewStore(defaultStoreRoot())
	origTotal, compTotal, n := st.Stats()
	if n == 0 {
		fmt.Println("headroom perf: store is empty")
		return 0
	}
	pct := savingsPct(int(origTotal), int(compTotal))
	fmt.Printf("entries:    %d\n", n)
	fmt.Printf("original:   %s\n", humanBytes(origTotal))
	fmt.Printf("compressed: %s\n", humanBytes(compTotal))
	fmt.Printf("savings:    -%d%%\n", pct)
	return 0
}

// --- helpers ---

// cmdExitCode extracts the exit status from a cmd.Run() error.
// Returns 127 if the command could not be started.
func cmdExitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	fmt.Fprintf(os.Stderr, "headroom: %v\n", err)
	return 127
}

// savingsPct computes integer percentage saved (clamped to 0 if comp >= orig).
func savingsPct(orig, comp int) int {
	if orig <= 0 {
		return 0
	}
	s := 100 - (comp*100)/orig
	if s < 0 {
		return 0
	}
	return s
}

// humanBytes formats a byte count as a human-readable string.
func humanBytes(n int64) string {
	const kb = 1024
	const mb = 1024 * kb
	switch {
	case n >= mb:
		return fmt.Sprintf("%.1f MiB", float64(n)/mb)
	case n >= kb:
		return fmt.Sprintf("%.1f KiB", float64(n)/kb)
	default:
		return fmt.Sprintf("%d B", n)
	}
}

// resolveMaxLines returns the effective max-lines: explicit flag wins, then
// HEADROOM_MAX_LINES env, then DefaultMaxLines.
func resolveMaxLines(explicit int) int {
	if explicit > 0 {
		return explicit
	}
	if v := os.Getenv("HEADROOM_MAX_LINES"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			return n
		}
	}
	return DefaultMaxLines
}
