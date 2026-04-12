# stt-for-i3

Speech-to-text for i3: press a key to start recording, press again to
transcribe and paste. Uses [whisper.cpp](https://github.com/ggerganov/whisper.cpp)
locally — no cloud, no latency surprises.

## Setup

You need these tools on your `$PATH`:

| Tool | Package | What for |
|------|---------|----------|
| `arecord` | alsa-utils | recording from mic |
| `whisper-cli` | whisper-cpp | local transcription |
| `xclip` | xclip | PRIMARY selection |
| `xdotool` | xdotool | simulating Shift+Insert |
| `dunstify` | dunst | notification updates |

Download a [whisper.cpp model](https://huggingface.co/ggerganov/whisper.cpp/tree/main)
(I use `ggml-small.bin`) and drop it into `~/.local/share/whisper/`:

```
mkdir -p ~/.local/share/whisper
curl -L -o ~/.local/share/whisper/ggml-small.bin \
  https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-small.bin
```

Override the model path with `WHISPER_MODEL=/path/to/model.bin` if you
want a different location. Set `WHISPER_THREADS=4` to limit CPU threads
during transcription (defaults to all cores).

## NixOS (flake)

Add the flake input and NixOS module to your configuration:

```nix
# flake.nix
inputs.stt-for-i3 = {
  url = "github:stapelberg/stt-for-i3";
  inputs.nixpkgs.follows = "nixpkgs";
  inputs.nixpkgs-unstable.follows = "nixpkgs-unstable";
};

# In your modules list:
stt-for-i3.nixosModules.default
```

Then enable the service:

```nix
# configuration.nix
services.stt-for-i3.enable = true;

# Optional: use a custom whisper-cpp build (e.g. with Vulkan GPU acceleration)
services.stt-for-i3.whisperPackage = pkgs.whisper-cpp.override { vulkanSupport = true; };
```

The module installs the binary, sets up a systemd user service, and wires all
runtime dependencies (`arecord`, `whisper-cli`, `xclip`, `xdotool`,
`dunstify`).

## Install (non-NixOS)

```
go install github.com/stapelberg/stt-for-i3/cmd/stt-for-i3@latest
```

### systemd user service

```
cp stt-for-i3.service ~/.config/systemd/user/
systemctl --user daemon-reload
systemctl --user enable --now stt-for-i3
```

## i3 keybinding

Add to your i3 config (`~/.config/i3/config`):

```
# Press to start recording, press again to transcribe and type.
# Press a third time during transcription to cancel.
bindsym $mod+comma exec --no-startup-id stt-for-i3 toggle
```

Reload i3 (`$mod+Shift+r`) and you're done.

## How it works

The daemon runs a three-state machine: **Idle** → **Recording** →
**Transcribing** → **Idle**. Each toggle press advances the state.
arecord captures 16kHz mono WAV, whisper-cli transcribes it, and the
result is pasted via PRIMARY selection + Shift+Insert.

If the daemon crashes during transcription, the WAV file survives in
`$XDG_RUNTIME_DIR`. On the next start, the daemon recovers it
automatically (transcribes and archives, but does not paste).

All recordings are archived to `~/stt/YYYY-MM-DD/recording-NNNNN/`
with the raw WAV and transcription metadata.

## Model evaluation

I evaluated 7 whisper.cpp models (v1.8.3) across 26 recordings of
natural English speech on an AMD 9950X3D. The recordings are real
dictation — ranging from 2-word test phrases to multi-sentence
paragraphs.

### Leaderboard

| Model | Avg time | Consensus match | Real errors |
|-------|----------|-----------------|-------------|
| `ggml-base` | 707ms | 38% | many |
| `ggml-small-q5_1` | 1,914ms | 42% | "Michael" → "my phone" |
| **`ggml-small`** | **2,177ms** | **46%** | **2 out of 26** |
| `ggml-medium-q5_0` | 5,694ms | 62% | rare |
| `ggml-large-v3-turbo-q8_0` | 7,954ms | 69% | rare |
| `ggml-large-v3-turbo-q5_0` | 8,473ms | 73% | rare |
| `ggml-large-v2-q5_0` | 10,558ms | 69% | rare |

The "consensus match" metric is strict — it compares exact normalized
text across all models, so even "gonna" vs "going to" or
"Bluetooth based" vs "Bluetooth-based" counts as a deviation. The
actual semantic accuracy is much higher across all models.

### ggml-small in detail

Out of 26 recordings, `ggml-small` produces 14 deviations from
consensus. 12 of those are cosmetic:

| Type | Example | Verdict |
|------|---------|---------|
| Contraction | "gonna" vs "going to" | irrelevant |
| Hyphenation | "Bluetooth based" vs "Bluetooth-based" | irrelevant |
| Filler words | "like" included or dropped | irrelevant |
| Singular/plural | "requests" vs "request" | irrelevant |
| Uncommon terms | "openwaite" vs "openweight" | nobody nails it |

The 2 real errors where meaning was lost:

| Recording | ggml-small said | Should be |
|-----------|----------------|-----------|
| 04-05/rec-04 | "this usage" (2x) | "disk usage" (2x) |
| 04-12/rec-04 | "of a workspace directory in memory directory" | "over workspace directories and memory directories" |

The larger models (`medium`, `large-v3-turbo`) fix these 2 errors but
cost 3-4x the latency (~6-8s vs ~2s). For interactive dictation where
you glance at the result and can re-dictate, 2s is the right tradeoff.

**Recommendation: `ggml-small`** — 2 real errors out of 26 recordings,
~2s transcription time. The next step up (`ggml-medium-q5_0` at ~6s)
isn't worth the wait for dictation.

## License

0BSD
