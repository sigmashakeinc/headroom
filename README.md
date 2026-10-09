# headroom — SigmaShake Context Compressor

headroom is SigmaShake's stdlib-only Go reimplementation of the headroom
concept: compress verbose command output / files **before** they reach the LLM,
60–95% fewer tokens, while keeping the original **retrievable on demand**
(reversible, lossless, content-addressed local gzip cache).

headroom is public at <https://github.com/sigmashakeinc/headroom> under the MIT
license. It runs on exactly three platforms: Windows x64, macOS arm64, and Linux x64.

headroom does **NOT** install a silent command-rewrite hook. Usage is always
explicit: `headroom <subcommand>`. There is no PreToolUse command-rewrite hook
and no env shim.

## What it does

| Subcommand | What happens |
|---|---|
| `headroom wrap <cmd> [args…]` | Runs the command, captures combined output, compresses it, stores the original, prints digest + footer. |
| `headroom compress [opts] [file\|-]` | Compresses a file or stdin, prints digest + footer. |
| `headroom get <handle>` | Prints the stored original, byte-identical. |
| `headroom perf` | Total original bytes, compressed bytes, savings %, entry count. |

### Compression engine

- **auto** — type-detects JSON, code, or log/text and routes accordingly.
- **json** — `json.Compact` + truncates long arrays (>20 elems) and strings (>200 chars) with explicit markers.
- **code** — language-agnostic lexical reduction: drops full-line comments (`//`, `#`, `--`, `;`), collapses blank runs, strips trailing whitespace. Conservative — lines containing a comment token anywhere other than position 0 are kept.
- **log / text** — re-implements STK's pipeline: CR strip → consecutive-duplicate collapse `(×N)` → blank-run collapse → importance-preserving truncation (error/warn/fatal lines are never dropped).

### Store

- Location: `~/.sigmashake/headroom/` (override with `HEADROOM_HOME`).
- Content-addressed: `sha256(original)` hex, first 12 chars. Identical content deduplicates.
- Originals are gzip-compressed to `.gz` blob files; `headroom get` gunzips back byte-identical.
- Prune on each `Put`: evicts entries older than 24 h **and** oldest-first if total blobs exceed 64 MiB.
- Wiping `~/.sigmashake/headroom/` only loses retrievability — it never corrupts past compressed digests.

## Install

Platforms: Windows x64, macOS arm64, Linux x64. No other platform is supported. On any other platform the installers exit with an error.

Linux x64 and macOS arm64:

```sh
curl -fsSL https://raw.githubusercontent.com/sigmashakeinc/headroom/main/install.sh | sh
```

Windows x64 (PowerShell):

```powershell
powershell -ExecutionPolicy ByPass -c "irm https://raw.githubusercontent.com/sigmashakeinc/headroom/main/install.ps1 | iex"
```

Go:

```sh
go install github.com/sigmashakeinc/headroom@latest
```

Installer details:

- `install.sh` installs to `$HOME/.local/bin`. Set `INSTALL_DIR` to change this.
- `install.ps1` installs to `%LOCALAPPDATA%\Programs\headroom`. It adds that directory to your user PATH. It needs no admin rights.
- Set `VERSION` (for example `v0.1.0`) to install a specific release.
- Both installers verify the SHA-256 checksum. They stop if it does not match.
- To install from a local archive, run `install.sh --from-file <archive.tar.gz> --checksums <checksums.txt>` or `install.ps1 -FromFile <archive.zip> -ChecksumsFile <checksums.txt>`.
- The env var `HEADROOM_CHECKSUMS_FILE` does the same as `--checksums` and `-ChecksumsFile`.
- With a checksums file, the installer checks the archive sha256 against the entry for its file name. It stops with exit code 1, and installs nothing, on a mismatch or a missing entry.
- Without a checksums file, a local install skips the check and prints a warning.

Check the install:

```sh
headroom --version
```

## Usage

```sh
# Compress go build output and store original:
headroom wrap go build ./...

# Compress a log file (auto-detect type):
headroom compress build.log

# Force JSON type:
headroom compress --type json response.json

# Compress stdin, stateless (no handle stored):
cat big-output.txt | headroom compress --no-store -

# Retrieve the original byte-for-byte:
headroom get abc123456789

# Print aggregate stats:
headroom perf

# Passthrough (no compression):
headroom wrap --raw go test ./...
HEADROOM_DISABLE=1 headroom wrap go test ./...
```

## Flags and env vars

| Flag / Env | Default | Description |
|---|---|---|
| `--raw` | off | Wrap passthrough — no capture, no compression |
| `HEADROOM_DISABLE=1` | off | Same as `--raw` |
| `--type auto\|json\|code\|log\|text` | `auto` | Force content type for `compress` |
| `--max N` | 500 | Max lines before truncation (log/text) |
| `HEADROOM_MAX_LINES=N` | 500 | Same as `--max`; flag takes precedence |
| `--no-store` | off | Stateless `compress` — no handle, no footer |
| `HEADROOM_HOME=<path>` | `~/.sigmashake/headroom/` | Store root override |
| `SIGMASHAKE_HOME=<path>` | `~/.sigmashake/` | Directory of the savings ledger `token-savings.jsonl` |
| `HEADROOM_SAVINGS=0` | on | Do not write the savings ledger |

## Exit codes

`headroom wrap` propagates the child's exit code exactly. Exit 127 means the
command could not be started.

## Running tests

```sh
bun run gate:test go test -C sigmashake-headroom ./... -cover
```

## Out of scope

These intentionally do NOT exist here:
- ML / HuggingFace text model
- KV-cache aligner
- Real per-language AST compressors
- LangChain / agno / image integrations
- Any proxy server or PreToolUse command-rewrite hook

Those require deps or GPU — both power-unsafe and in violation of stdlib-only.

## Source of truth

The SigmaShake monorepo is the source of truth for this tool. A mirror script
syncs it to <https://github.com/sigmashakeinc/headroom>.

## Contributing

SigmaShake maintains this repository. The repository does not accept outside issues or pull requests. Issues and outside pull requests are disabled.

## License

MIT. See `LICENSE`.
