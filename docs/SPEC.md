# QS-PodScript – spec & design decisions

Go app that transcribes one podcast feed episode by episode (oldest first) and
labels who said what. Replaces the old Whisper LXC + PPP scripts. Target users
include non-technical people.

## Architecture
- Go app = orchestrator. Heavy lifting by tools it downloads itself into
  `data/tools` and `data/models` (no system-wide installs, no admin rights):
  - ffmpeg (audio conversion, compact audio copy)
  - whisper.cpp `whisper-cli` (transcription; CUDA build on NVIDIA, else CPU)
  - sherpa-onnx (linked via Go bindings/DLL): speaker diarization + voice embeddings
- Portable: everything lives in `data/` next to the executable. Delete folder = uninstall.
- Storage: one SQLite file `data/qs-podscript.db`.
- UI: local web UI served by the Go app on 127.0.0.1 (html/template + CSS +
  vanilla JS, no frameworks/CDNs, embedded via go:embed), opened in the
  browser; double-click the exe = start. The CLI stays for power use/debugging.
  Forms work without JS; JS adds live status (SSE) and audio interaction.
- Windows first (tested on GTX 1660 Super), then Linux, maybe macOS.

## Key decisions
- **Transcription in pieces (v0.7.0).** Speaker detection runs first; the
  whole episode is cut into <= 28 s pieces at pauses/speaker changes and each
  piece is transcribed separately (one whisper-cli run). Whisper can't skip
  or loop across windows. Nothing is dropped: no-text pieces with speech ->
  "[unclear]"; low-confidence words shown as uncertain.
- **Corrections survive new versions** (people, crosstalk, unknown, text) as
  time ranges on the same audio timeline.
- **Store raw results, not text.** Whisper segments + tokens (timestamps),
  diarization turns, per-cluster voice embeddings. Readable transcript is built
  at display time (merge.go). → instant speaker relabeling, re-diarize without
  re-transcribing.
- **Versions.** Re-processing an episode creates a new version; old ones are
  kept. Episode points to one active version.
- **Pinned tool versions** (whisper.cpp v1.9.2, sherpa-onnx v1.13.8) – never "latest".
- **Diarization errs towards too many clusters** (threshold 0.3). One person
  split into 2 clusters gets fixed by voice matching later; two people merged
  into one cluster can't be undone.
- **Keep a compact audio copy per version** (Opus 24 kbit/s, ~11 MB/hour).
  Dynamic ad insertion changes the audio per download, so timestamps only match
  the exact file that was processed. Needed for click-to-play and speaker review
  snippets. Setting `keep_audio`.
- **Voice embeddings per cluster are computed at processing time** (up to 30 s
  of clean, non-overlapping speech per cluster) so episodes processed now can be
  used for speaker learning later without re-running.
- Crosstalk = token time ≥50% inside a region where 2+ clusters talk.
- Queue order: priority DESC, pub_date ASC. Ctrl+C once = finish current
  episode then stop; twice = abort (episode requeued, half version deleted).
- whisper-cli is run from the episode work folder with relative paths
  (non-ASCII absolute paths on Windows are a risk).

## Future-proofing for distributed mode (phase 6)
- Keep raw results (not text) and stable episode identity (feed guid) – they
  become the upload format and the server-side dedupe key.
- Voiceprints should be exportable/importable per person, so they can be
  shared centrally later.
- Search "jump to" with dynamic ads: start 1-2 min before the stored timestamp
  and show the matching text; possibly store ad-corrected "content time" too.

## Planned speaker learning (phase 3)
- Speakers are global people, not per-podcast labels: hosts appear on several
  covered podcasts. Voiceprints belong to the person; a podcast lists its
  regulars. Matching checks the podcast's regulars first, then everyone known.
- Per-episode participant names from the user are treated as certain presence:
  each listed person must get a cluster (else flag for review); unlisted people
  need a clearly higher score. Names can be pre-filled from RSS show notes.
  Lists are never exclusive – lineups vary.
- Podcast roster has tiers: hosts (almost always), pool (rotating panel, any
  subset), everyone else. Call-in voices are labelled "Caller n" and are never
  used to train voiceprints.
- After diarization, each cluster's embedding is compared (cosine) with known
  speakers' voiceprint sets.
- Confident match → auto label. Unsure → review queue (doesn't block processing).
- Review page: per unknown cluster a few audio snippets + name dropdown
  (host names / Guest / Ad-ignore). Keyboard shortcuts.
- Confirmed matches (and very confident auto matches) add that episode's
  embedding to the speaker's set → improves over the years (voice/mic drift).
  User can view/remove bad samples.

## Phases
1. Core pipeline CLI (Windows + Linux) ✓
2. Web UI: feed setup, queue with live progress, episode list, transcript view ✓
3. Speaker review + learning
4. Versions compare / edit
5. Installer polish, Linux GPU, macOS
6. Distributed mode: local apps pull work from + upload to a homeserver backend; public transcript search

## Homeserver design (confirmed by Batz 2026-09-27: same program in server mode, workers upload the audio; editing on the server only)

Core idea: the server is **the same program in server mode**
(`qs-podscript server`), not a second codebase. It reuses the database,
the episode pages, corrections, people, spelling fixes, identification –
and adds logins, rights and an API for workers. Less new code, one UI.

### Roles
- **Server** (LXC on the homelab, behind the existing reverse proxy / HTTPS):
  the one place where the shared podcasts live – feeds, episode list,
  transcripts, corrections, people, voice samples. Normally transcribes
  nothing itself (can, if it has a GPU).
- **Worker** = a local QS-PodScript with "Connect to server" switched on.
  It takes episodes from the server, transcribes them with its own graphics
  card and uploads the result. Its own local-only podcasts keep working as
  today.
- **Browser users**: public read-only view; logged-in editors fix speakers,
  text, marks for the podcasts they have rights for; admins manage users,
  podcasts and workers.

### Work flow
1. Worker asks for work → server leases the oldest untranscribed episode
   (lease ~6 h, extended while the worker reports progress, freed on expiry).
2. Worker downloads the audio itself, transcribes with the server's
   settings (model, pieces, speaker detection – so all results are alike).
3. Worker uploads the raw result: segments, tokens, speaker turns, voice
   embeddings, timings, app/model info **and the 24 kbit audio copy**
   (~11 MB/h). The audio must come from the worker: with dynamic ads every
   download differs, and the timestamps only fit the file that was
   transcribed.
4. Server checks it (garbage/loop check, length), stores it as a new version,
   runs speaker identification with the central voiceprints, and carries
   over corrections if the episode had an older checked version.

### Editing server podcasts from the local app: pass-through (Batz, 2026-09-28)
A connected local app lists the server's podcasts and serves the server's
pages on a second local port (127.0.0.1:<port+2>), adding its API token –
same workflow as local podcasts, but every read and change goes straight to
the server. The pass-through only forwards POSTs whose Origin/Referer is its
own address (another website must not be able to use the stored token).

### No two-way sync of transcripts
Editing happens in one place – on the server (in the browser). A connected
local app doesn't keep copies of server podcasts, so nothing can conflict.
People and voice samples live on the server; workers don't need them
(identification runs on the server). Existing local work goes to the server
once via the export file (server imports it into its database).

### API (JSON over HTTPS, token per worker)
- `POST /api/v1/work/claim` → episode (id, audio URL, podcast settings) or none
- `POST /api/v1/work/{lease}/progress` → keeps the lease alive
- `POST /api/v1/work/{lease}/result` (multipart: result.json + audio.ogg)
- `POST /api/v1/work/{lease}/fail` → episode back to the queue, error noted
- Server refuses workers below a minimum app version.

### Users and rights
- admin: everything; creates users and worker tokens.
- editor: may edit transcripts of the podcasts assigned to them.
- everybody else: read-only (public pages; login not needed).
Sessions via cookie (login form, no JS), passwords stored with bcrypt.

### Order of work
1. Server mode: logins, users/rights, read-only public pages, editors can edit. – done v0.12.0
2. Worker API + "Connect to server" in the local app + pass-through. – done v0.13.0
3. Import of an export file on the server (seed with Batz's existing work).
4. Search (next TODO step) builds on the server's database.
