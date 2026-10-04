# QS-PodScript – technical overview (for planning)

State: v0.35.0 (2026-10-04). A short brief of what the program does under the
hood, to discuss future steps. The UI is deliberately left out. Repository:
https://github.com/baumbaTz/qs-podscript (AGPL-3.0). Author/operator: Batz.

## What it is

One Go program (~20k lines, package `main`, cgo) that turns podcast episodes
into transcripts with **speaker attribution** ("who said what"), keeps them
searchable, and lets people correct them. It runs in two modes from the same
binary:

- **Local app** (`qs-podscript serve`): runs on a PC, ideally with a GPU,
  transcribes its own podcasts and can work as a **helper** for a server.
- **Server** (`qs-podscript server`): shared homeserver (Batz:
  transcribe.quicksack.li, Debian LXC behind Traefik). Public read-only
  pages, logins (admin / editor per podcast), a work queue that helpers
  take jobs from. Usually transcribes nothing itself.

Purpose: transcripts for the quicksack.li podcast family (Film Sack, Wait,
You Haven't Seen…?, GORE, …) – roughly 1,300 episodes, mostly 1–2 h long.

## Building blocks

| Part | What |
|---|---|
| ffmpeg | download conversion to 16 kHz mono WAV; a compact Opus copy (24 kbit/s, ~11 MB/h) is kept per version for playback |
| whisper.cpp v1.9.2 (`whisper-cli`, external process) | speech-to-text; CUDA / Vulkan / Metal / CPU builds, downloaded or compiled by the installer; models turbo (default), large-v3, etc. |
| sherpa-onnx v1.13.8 (Go binding, shared libs) | speaker diarization (pyannote segmentation + a speaker-embedding model, 9 to choose from, e.g. ResNet34, TitaNet large) and voice embeddings; CPU, or NVIDIA GPU on Windows / Linux via sherpa-onnx's CUDA build, downloaded by the app itself (Setup button, `qs-podscript gpu-speakers install`, offered by both installers; ~10× faster in a first test) |
| SQLite (mattn/go-sqlite3, FTS5) | everything in one file `data/qs-podscript.db` |
| Web | Go `html/template`, own CSS, minimal vanilla JS (forms work without JS), SSE for live status; strict-CSP compatible |

Everything the program needs is downloaded into `data/` next to the binary
(portable, no admin rights). Tool versions are pinned, never "latest".

## Pipeline per episode

1. **Download** the enclosure; **convert** to 16 kHz WAV (+ Opus copy).
2. **Speaker diarization** over the whole episode (sherpa-onnx): segmentation
   with a sliding window (step 2.5 s default), embeddings per window,
   clustering with a threshold per model. Tuned to rather split one person
   into two clusters than merge two people (splits are fixable later,
   merges are not). One voice embedding per cluster is stored.
3. **Cut into pieces ≤ 28 s** at pauses / speaker changes; **whisper.cpp per
   piece** (one process run). Prevents Whisper's long-audio failures
   (skipping, loops). Empty pieces with speech become "[unclear]".
4. **Sanity check** of the text (garbage like "!!!!", loops); bad results
   are never stored silently.
5. **Voice merge** (optional): clusters with very similar voiceprints are
   merged automatically.
6. **Identification**: every speaker turn is compared with **voice samples
   of known people** (cosine similarity ≥ 0.55, margin to the second best,
   stricter for people not on the podcast's roster). Voice samples come from
   passages users confirmed.
7. **Store raw results** as a new **version**: Whisper segments + tokens with
   timestamps, diarization turns, cluster embeddings, step timings.

Typical times on an RTX 3070: Whisper ~6 min per long episode; speaker
detection ~4 min on the GPU (was up to ~50 min on the CPU).

## Data model (core ideas)

- `feeds` (+ `feed_sources`: several feeds per podcast), `episodes`
  (identity = feed GUID), `versions` (every (re)run is a new version; the
  episode points to the active one).
- Raw results per version: `segments`, `tokens`, `turns`,
  `cluster_embeddings`.
- **The readable transcript is never stored.** It's built at display time
  (`merge.go`): tokens are labelled by time with the diarization turns,
  lines are split at speaker changes, then **corrections** are applied on
  top.
- `corrections` per version, on the audio timeline: `merge` (cluster A is
  B), `range` (this time span is person X / unknown / crosstalk), `text`,
  `mark` (ad / movie clip). Later ones win. Because they're time-based,
  they are **carried over** to a new version of the same audio
  (`carry.go`), so re-transcribing doesn't lose work.
- `people`, `voice_samples` (embeddings of confirmed passages),
  `feed_people` (roster: hosts / regulars), `episode_people` (who can
  appear in an episode), `checks` (which stretches people confirmed),
  `spelling` (per-podcast text fixes).
- Labels: cluster numbers ≥ 0, people = labels ≥ 1,000,000
  (`personLabel(id)`), −1 unknown, −2 crosstalk.
- Search: FTS5 index over passages of ~30 words, built in the background;
  plain LIKE fallback without FTS5.
- Server side: `users`, `user_podcasts`, `sessions`, `api_tokens` (one key
  per helper computer), `user_activity`.

## Server ↔ helper protocol

JSON over HTTPS, bearer token per computer (`qs-podscript connect` or the
installer creates it with a one-time login):

- `POST /api/v1/work/claim` → the next episode + the server's settings, as a
  **lease** (~6 h, extended by progress reports, freed on expiry).
- `…/{lease}/progress`, `…/{lease}/result` (raw results + the Opus copy –
  the audio must come from the helper, because dynamically inserted ads make
  every download different and timestamps only fit the file actually
  processed), `…/{lease}/fail`.
- The server validates, stores a new version, runs voice merge,
  identification (central voiceprints) and carries corrections over.
- Helpers never start work by themselves; they work when their user presses
  start or asks for a specific episode ("asks" are queued and claimed
  specifically). Minimum app version is enforced; helper and server should
  run the same version.
- A local app can show and edit a server's pages through a local
  **pass-through** port that adds its token (no copies, no sync – editing
  happens on the server only).

## Deployment and release

- Linux `install.sh` (desktop or `--server` with a systemd service;
  `--gpu-speakers`; `--connect` to join a server), Windows `install.ps1`
  (`-GpuSpeakers`, `-Connect`), macOS zip (untested). Settings and data
  survive updates.
- GPU speaker detection (`gpuspeakers.go`): sherpa-onnx's CUDA build +
  NVIDIA's CUDA 13 / cuDNN 9 wheels from PyPI (+ the MSVC runtime on
  Windows), all pinned with SHA-256, into `data/tools/onnxruntime-gpu`,
  `<install>/cuda` and next to the binary; the CPU libs are saved for
  "remove". Loaded libraries are never overwritten in place; an update that
  brings the CPU libs back is fixed at the next start. Diarization runs in a
  child process, so it uses the card right after installing.
- GitHub Actions build Linux / Windows / macOS packages on a `v*` tag; the
  tag must match `const version` in `main.go`. Development happens in
  sandboxed sessions; Batz applies a "changes zip" and pushes.
- Batz's helpers: PCs with NVIDIA cards (RTX 3070 on CachyOS with GPU
  speaker detection working; Windows GPU speaker detection new in 0.35.0, not
  tested on a real Windows NVIDIA PC yet).

## Known limits / open ends (as of 0.35.0)

- GPU speaker detection is NVIDIA-only and needs compute capability 7.5+
  (RTX 20 / GTX 16 or newer; CUDA 13 dropped older cards): sherpa-onnx has no prebuilt
  DirectML (Windows AMD/Intel) or ROCm build, and on macOS it stays on the
  CPU. Windows NVIDIA support is new and untested on real hardware.
- Speaker attribution quality depends on diarization + voice samples;
  identification is per turn, there is no language-model or
  context-based speaker reasoning.
- No real-world accuracy measurement (WER / speaker error) on the actual
  podcasts yet; the model/threshold defaults come from a synthetic test.
- macOS untested on real hardware.
- One SQLite file on the server; one server instance (no HA).
