# Antigravity Doctor

Your Antigravity conversations disappeared? **This tool brings them back.** No reboot required.

## Why existing tools don't work

The popular [antigravity-conversation-fix](https://github.com/FutureisinPast/antigravity-conversation-fix) only patches `trajectorySummaries` — a legacy database key. Since **Antigravity v1.100+**, the sidebar reads from a different key: `chat.ChatSessionStore.index`. That tool never touches it, which is why conversations keep disappearing.

**Antigravity Doctor patches both keys**, plus workspace-specific databases, annotations, and cleans up stale temp files.

## Quick Start

### Download (Recommended)

1. **Close Antigravity** completely (File → Exit)
2. Download the binary for your OS from [Releases](../../releases)
3. Run it — done
4. Reopen Antigravity — your conversations are back

**No Python. No reboot. No dependencies. One file.**

| Platform | Binary |
|---|---|
| Windows x64 | `antigravity-doctor-windows-amd64.exe` |
| macOS Intel | `antigravity-doctor-darwin-amd64` |
| macOS Apple Silicon | `antigravity-doctor-darwin-arm64` |
| Linux x64 | `antigravity-doctor-linux-amd64` |

### Python Version

A standalone Python version is available in [`python/`](python/) for environments where a pre-compiled binary isn't practical. Requires Python 3.7+, no external packages.

```bash
python python/antigravity_doctor.py
```

## What it fixes

| Problem | Fixed? |
|---|---|
| Conversations disappear after restart | ✅ |
| Conversations not in sidebar | ✅ |
| Wrong sort order | ✅ |
| Missing/placeholder titles | ✅ |
| Lost workspace assignments | ✅ |
| Ghost annotations (metadata for deleted chats) | ✅ |
| Orphan conversations (data exists but invisible) | ✅ |
| Stale `.tmp` files from failed writes | ✅ |

## How it works

Antigravity stores conversation data in two places:

```
~/.gemini/antigravity/conversations/*.pb    ← actual chat data (protobuf)
%APPDATA%/Antigravity/.../state.vscdb       ← SQLite index (sidebar reads this)
```

When the index gets corrupted (empty `ChatSessionStore.index`), conversations still exist on disk but don't show in the sidebar. This tool:

1. Scans all `.pb` files on disk
2. Resolves titles from brain artifacts and existing metadata
3. Auto-detects workspace assignments
4. Rebuilds **both** `trajectorySummaries` AND `ChatSessionStore.index`
5. Patches workspace-specific databases
6. Creates missing annotation files
7. Cleans stale temp files

## Comparison

| Feature | [antigravity-conversation-fix](https://github.com/FutureisinPast/antigravity-conversation-fix) | **Antigravity Doctor** |
|---|---|---|
| `trajectorySummaries` fix | ✅ | ✅ |
| `ChatSessionStore.index` fix | ❌ **Missing** | ✅ |
| Workspace-specific DB fix | ❌ | ✅ |
| Annotation repair | ❌ | ✅ |
| Temp file cleanup | ❌ | ✅ |
| Requires reboot | ⚠️ Yes | ✅ **No** |
| Requires Python | ⚠️ Yes (or 40MB PyInstaller exe) | ✅ **No** |
| Binary size | ~40 MB | **~8 MB** |
| Antivirus false positives | ⚠️ PyInstaller flagged | ✅ None |
| Startup time | 2-3 sec (temp extraction) | **Instant** |

## CLI Flags

```bash
# Standard fix (default)
antigravity-doctor

# Remove ghost annotations (metadata for deleted conversations)
antigravity-doctor --clean-ghosts
```

## Building from source

```bash
# Requires Go 1.21+
go build -ldflags="-s -w" -o antigravity-doctor .

# Cross-compile for Windows from any OS
GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o antigravity-doctor.exe .
```

## Safety

- **Automatic backup** — `state.vscdb` is backed up before any changes
- **Non-destructive** — conversation files (`.pb`) are never modified
- **Idempotent** — safe to run multiple times

## Project Structure

```
antigravity-doctor/
├── main.go              # Entry point, orchestration, CLI
├── metadata.go          # Title resolution, workspace inference, DB ops
├── paths.go             # Cross-platform path detection
├── protobuf.go          # Minimal protobuf varint encoding
├── hide_windows.go      # Windows console hiding (build-tagged)
├── hide_other.go        # No-op stub for Mac/Linux
├── go.mod               # Go module definition
├── python/              # Standalone Python version
│   ├── antigravity_doctor.py
│   └── run_doctor.bat
├── .github/workflows/
│   └── release.yml      # CI: auto-builds binaries on tag push
├── README.md
└── LICENSE
```

## License

MIT — free to use, share, and modify.
