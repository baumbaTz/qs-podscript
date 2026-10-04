# TODO

Cleaned up 2026-09-27: only what Batz decided to keep. Order = planned order.
Updated 2026-10-04 (v0.35.0) - see HANDOFF.md for the overall state.

## Next up (open points as of v0.31.2)
- [ ] Batz: look at the episode page, quiz and voice page on a real phone
      once (0.31.2 checked them at 412 px in a test browser).
- [ ] Batz: on the server, update with ./install.sh (finds the existing
      service, keeps --listen/--trusted-proxy). Optional: add the CSP header
      from README-linux.txt to the Traefik secure-headers middleware.
- [ ] Batz: look at QuickSack bright mode on the real server (button bottom
      left) and say if any colour should change.
- [ ] Batz: post a podcast/episode link in Discord and check the preview
      (Discord caches previews - test with a link it hasn't seen yet).
      Optional: submit https://transcribe.quicksack.li/sitemap.xml in
      Google Search Console.
- [ ] Batz: try the installer's server question on Windows once (hidden
      password input could only be tested on Linux; the rest of the
      Windows part was tested with PowerShell 7).
- [x] Batz: GPU speaker detection on the 3070 PC works (0.34.3: GPU-CHECK OK
      cpu=131ms gpu=19ms; ~4 min instead of ~49 for a long episode).
- [ ] Batz (0.35.0): on the CachyOS PC run ./install.sh of 0.35.0 - it should
      find the existing GPU part, download nothing and say OK. On a Windows
      PC with an NVIDIA card (driver 580+): Setup -> Speaker detection ->
      "Use the graphics card for speaker detection" (or install.cmd), then
      check the Setup line / "qs-podscript gpu-check". First real Windows
      test of this - send data\qs-podscript.log if it fails.
- [ ] Batz: check a full GPU-processed episode for quality (should equal
      the CPU result; cos=1.00000 in the check).
- [ ] Possible: finer detection step (1 s) now that the graphics card makes
      it cheap.
- [ ] Optional: CF-Connecting-IP support (only if a Cloudflare proxy is ever
      put in front of the server).
- [ ] Dependabot PRs: merge one at a time after CI is green.

## GitHub (v0.29.0)
- [x] Repo baumbaTz/qs-podscript, Vulkan whisper-cli.exe deps release,
      release workflow builds Linux/Windows/macOS/source on a v* tag.
- [x] Screenshots redone for 0.34.4 (QuickSack, logo) - still the made-up
      demo podcast; real ones (Film Sack etc.) only if wanted.

## Ongoing
- [ ] Windows packages: compiled with every build (catches breakage), but only
      delivered/tested again at the end (Batz, 2026-09-27) - same as macOS.
      2026-09-28: Batz tested 0.15.x standalone on Windows - works. Server
      client (connect / pass-through / transcribe for server) still untested.
- [ ] Keep the manual (web/templates/help.html) in step with every UI change.

## 1. Several feeds per podcast – done in v0.10.0
- [ ] Batz: add the archive feeds of the real podcast and check the episode
      count / duplicates. Report episodes that show up twice (title + both dates).

## 2. Export / import – done in v0.11.0
- [x] Batz: export from the local install, import on the server - works.

## 3./4. Homeserver + connecting local installs
Design: docs/SPEC.md "Homeserver design".
- [x] Server mode, logins, users + rights, public read-only pages (v0.12.0)
- [x] Worker API (login -> token per computer, claim / progress / result + audio /
      fail, leases with expiry and failure count, minimum app version),
      server-side checks and storing (version, voice merge, carry-over,
      identification) (v0.13.0)
- [x] Local app: connect / disconnect, "Transcribe for the server", server
      podcasts on the Podcasts page, pass-through editing (v0.13.0)
- [x] Own password, computers (keys) with revoke, record of who transcribed /
      cleaned up what, activity log (v0.14.0)
- [x] Admin overview of reserved episodes, hand back / try again (v0.15.0)
- [x] Credits shown publicly, anonymous ("Volunteer N") unless the user opts in (v0.15.0)
- [x] Batz: server installed + started on Debian trixie (0.15.1)
- [x] Batz: reverse proxy (Traefik) set up (transcribe.quicksack.li ->
      LXC :8322). From inside the LAN Batz uses the LAN IP
      (http://192.168.178.72:8322) - router loopback to the public IP on 443
      was refused (cause not found: not fail2ban, not the app).
- [x] Connect PC, transcribe for the server, edit through the pass-through:
      in daily use (Film Sack, WYHS, GORE).

## 5. Homeserver web frontend
- [x] Public read-only view of all transcripts - cleaned up for visitors (v0.25.0).
- [x] Multi-user: admins create users and give them edit rights for specific
      podcasts, so volunteers can fix speakers/text in the browser without
      using their own computer's power.

## 6. Public search engine
- [x] Full-text search over all covered podcasts (SQLite FTS5), filter by
      podcast / person, results link to the spot in the episode (v0.25.0).
- [x] Leave ads out of the index, keep clips flagged; apply spelling fixes.
- [ ] Batz: try on the real server - first index build time, result quality.
- [ ] Maybe: episode titles/show notes searchable too; link to share a
      search; "more from this episode" grouping.

## 7. macOS version (Metal)
- [x] Code: whisper.cpp + ffmpeg from Homebrew (Metal by itself), found also
      when started from the Finder; Setup texts; "metal" device (v0.29.0).
- [x] Build on GitHub Actions macOS runner (build-macos.sh, release workflow):
      Apple Silicon zip with the sherpa-onnx dylibs next to the program.
- [x] Unsigned-app hint (Gatekeeper) in README-macos.txt.
- [ ] Tester with an M3 Mac (via Batz): first real run.

## Speaker detection models + graphics card (v0.16.0)
- [x] Reasonable default thresholds for all models (v0.28.0): real-audio
      values for ResNet34 (0.5) and TitaNet large (0.96), estimates for the
      others. Fine-tune only if one of them gets used for real.
- [x] GPU speaker detection works (0.34.3, RTX 3070: ~10x faster).
- [x] Windows NVIDIA part + install from Setup instead of only install.sh
      (0.35.0).
- [ ] AMD/Intel cards: no prebuilt sherpa-onnx with DirectML (Windows) or
      ROCm/MIGraphX (Linux) - would mean building sherpa-onnx + ONNX Runtime
      ourselves (CI job). Only if speaker detection time matters there.
- [ ] Browser-side processing experiment: tabled (2026-10-04).

## Look Who's Talking (was "Who's talking?", v0.17.0)
- [ ] Batz: try it on a real episode; is ~30 s a good passage length? Are the
      name buttons the right ones (hosts + regulars)?
- [x] Carry checks over to a new version of the same audio (v0.17.1).
- [x] Split a line inside the quiz (v0.17.2).

## Server work (v0.19.0)
- [x] Batz: update server + both PCs to 0.19.0; "Redo speaker detection" on
      two episodes -> both PCs (Transcribe for the server) pick one each.
- [ ] Maybe: import into a server keeps its users/keys (only takes the
      podcast data) - so logins survive.

## Voice model thresholds (real audio)
- [x] TitaNet large 0.96 (Batz); others: estimated defaults (v0.28.0).

## Several servers (v0.26.0)
- [x] Batz: update server + PCs; check the place menu, switching, the
      "Server" tabs in Setup/Activity, and artwork of the real podcasts.

## Optional
- [ ] Heavier speaker detection engine (pyannote / PyTorch) - strictly
      optional, only if the current quality isn't good enough; must not make
      the normal download bigger for everyone.
- [ ] Vocabulary hint for whisper (per podcast; roster names + spelling fixes'
      correct spellings as --prompt). Needs -mc > 0 in piece mode (with -mc 0
      whisper.cpp drops the prompt) - test against loops and the song
      episodes; risk: names hallucinated into music/noise.
