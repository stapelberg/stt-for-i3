# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build and Test

```bash
go install ./cmd/stt-for-i3    # install binary to $GOBIN
go test ./...                   # run all tests
go test ./internal/stt/         # run tests for the stt package
go test ./internal/stt/ -run TestNextRecordingNumber  # run a single test
```

Nix: `nix build` produces the package. After `.nix` file changes: `nix fmt && nix build`.

## Architecture

Single-binary daemon (`stt-for-i3 daemon`) controlled via Unix socket commands (`stt-for-i3 toggle`). The i3 keybinding invokes `toggle`; the daemon runs as a systemd user service (`Type=notify`).

**State machine** (Idle → Recording → Transcribing → Idle): each toggle press advances the state. A third press during transcription cancels it. All state transitions are serialized under `Daemon.mu`.

**Key design points:**
- `Daemon` (daemon.go) owns the state machine, Unix socket listener, signal handling, and crash recovery. Generation counter (`d.gen`) disambiguates stale transcription goroutines after cancel+re-record cycles.
- `Recorder` (recorder.go) wraps `arecord` with Start/Stop/Done channels. A death-watcher goroutine detects unexpected arecord exits.
- `Transcribe` (transcriber.go) shells out to `whisper-cli`. `Archive` copies WAV + metadata to `~/stt/YYYY-MM-DD/recording-NNNNN/`. `Paste` uses xclip + xdotool to inject text via PRIMARY selection.
- `Notifier` (notify.go) wraps `dunstify` with replace-ID tracking for updating a single persistent notification.
- Crash recovery: if a WAV file exists in `$XDG_RUNTIME_DIR` at startup, the daemon transcribes and archives it (but does not paste) before signaling `READY=1`.

**External tools** (must be on `$PATH`): `arecord`, `whisper-cli`, `xclip`, `xdotool`, `dunstify`.

## Environment Variables

- `WHISPER_MODEL` — override model path (default: `~/.local/share/whisper/ggml-small.bin`)
- `WHISPER_THREADS` — limit CPU threads for whisper-cli (default: all cores)
