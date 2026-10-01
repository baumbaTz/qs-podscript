# Releasing

Releases are built by GitHub Actions (`.github/workflows/release.yml`) on
GitHub's runners – Linux and Windows on Ubuntu, macOS on an Apple Silicon
runner (free for public repositories).

## Once: repository settings

- **About** (gear next to "About" on the repo page):
  - Description: *Podcast transcripts with speaker names – on your own computer or shared by volunteers. whisper.cpp + sherpa-onnx, Go.*
  - Topics: `podcast` `transcription` `speaker-diarization` `whisper` `whisper-cpp` `sherpa-onnx` `speech-to-text` `golang` `self-hosted` `search`
  - Tick "Releases", untick "Packages"/"Deployments" if unused.
- **Settings → General → Social preview**: upload `docs/social-preview.png`.
- **Settings → General → Features**: Issues on; Discussions optional.
- **Settings → Code security**: enable *Private vulnerability reporting*
  (SECURITY.md points there) and *Dependabot alerts*.
- **Settings → Actions → General**: *Workflow permissions* "Read and write"
  is not needed – the release workflow asks for `contents: write` itself.
- **Settings → Branches** (optional): protect `main`, require the CI checks.

## Once: the Windows Vulkan build of whisper.cpp

whisper.cpp publishes no Windows build with Vulkan (AMD/Intel cards), so
QS-PodScript ships its own `whisper-cli.exe` (recipe:
`third_party/whisper-vulkan-win64/BUILD.md`). It is too big for the git
repository (43 MB), so it lives as an asset of a separate release:

1. GitHub → Releases → **Draft a new release**
2. Tag: `deps-whisper-vulkan-1.9.2` (create it on `main`), title
   "Build dependency: whisper.cpp 1.9.2 Vulkan for Windows"
3. Attach `whisper-cli.exe` (sha256
   `781212e268e643ad8cf4a750dfce7dba4fc54c0cf791891ab44eac6fd8115864`)
4. Tick **Set as a pre-release** (so it never shows up as "latest"), publish.

The release workflow downloads it from there and checks the checksum. If it
is missing, the Windows package is built without Vulkan (a warning in the
workflow log). A new Vulkan build: new tag name, then update
`VULKAN_WHISPER_URL` and `VULKAN_WHISPER_SHA256` in the workflow.

## Every release

1. `const version` in `main.go` → the new version (e.g. `0.29.1`).
2. A section in `DONE.md`: `## <date> – v0.29.1 – <title>` – it becomes the
   release notes.
3. Commit, push, wait for CI to be green.
4. Tag and push the tag:
   ```sh
   git tag v0.29.1
   git push origin v0.29.1
   ```
5. The **Release** workflow checks that the tag matches `main.go`, runs the
   tests, builds Linux, Windows and macOS packages plus the source zip, and
   publishes the release with `SHA256SUMS.txt`.

A test build without publishing: Actions → Release → **Run workflow** – the
packages appear as downloadable artifacts of that run.
