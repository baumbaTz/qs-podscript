# DONE

## 2026-10-08 – v0.39.0 – click / double-click on words, split marks between words, "Get started" page, manual up to date
- Episode page (app.js): a **click** (or tap) on a word opens its menu (who said it,
  correct it, play from here, ad/clip mark); a **double-click** only plays from
  there (starts at the full second before the word) and never shows the menu. A click
  waits 250 ms to tell the two apart. On a touch screen a long press selects the
  word, which opens the same menu as before. Words that are selected keep their own
  menu; speaker names and times behave as before.
- Speaker check (Look Who's Talking / line by line, voice.html + quiz.html): the line is
  split at a thin **bar between two words** instead of by clicking the first word of
  the new speaker. Mouse: the bars are faint while the pointer is over the line and
  clear when over the gap; touch screens have no hover, so the bars are always shown
  and have a bigger target. They add no width (no jumping text). After a split both
  parts get fresh bars. Clicking a word no longer splits.
- New page **/start** ("Get started", public; new users land there after joining
  with an invite link): browser-only helping vs. lending the computer, and four steps
  (download, install, connect to this server with its address and the user's name,
  start). The download buttons (Windows / Linux / macOS) point straight to the files
  of the newest release: release.go asks the GitHub API (cached 6 h, retried after
  10 min on errors, only real app releases – not drafts, prereleases or the
  deps-whisper-vulkan release); if GitHub can't be reached the links are built from
  this server's own version. Kept out of search engines (robots.txt).
- Manual (help.html): the new gestures and split bars; Podcast settings are now tabs;
  search box in the top bar; Setup/Queue in the place menu while on a server; invite
  links, user pages and the Get-started page; new section "Putting the data on
  another drive" (symlink on Linux / server, junction on Windows, Proxmox mount point
  or resize).
- Tests: release_test.go (which release is picked, cache, failed refresh, fallback
  links); invite_test.go now also opens the users, user, podcast, help, start, account,
  search and activity pages through the real handlers as admin and as editor and
  requires them to come out whole (a template error used to leave a 200 with a cut-off
  page that substring checks didn't notice), and checks the settings tabs per role.

## 2026-10-07 – v0.38.0 – invite links with a QR code, for adding helpers without setting their password yourself
- New file invite.go: invites table + invite_podcasts table (schema v20), NewInvite /
  Invites / inviteByToken / RevokeInvite / AcceptInvite on Store, and the routes:
  POST /invites (admin, creates one), POST /invites/{id}/revoke (admin),
  GET+POST /join/{token} (public, no login – the person has no account yet).
- The admin fixes which podcasts the invite grants *when creating it*; the QR
  code and link only ever carry the token, never the rights themselves. The
  person who opens the link only picks a username and a password – they're
  always created as an editor, never admin, with exactly those podcasts.
  Invites are single-use (consumed in the same DB transaction as account
  creation, so two people can't race one link) and expire after 7 days.
- QR code: added github.com/skip2/go-qrcode (pure Go, no cgo) purely as the
  encoder; invite.go reads its module Bitmap() and renders that as inline
  SVG itself (no JS, no external image request, matches the rest of the
  server-rendered SVG in the app).
- users.html: "Invite a helper" panel (tick the podcasts, Create invite);
  right after creating one, the link + QR + "can edit: …" are shown once;
  an "Open invites" list (who made it, when it expires, which podcasts) with
  a Revoke button per invite. The old "Add a user" form (set the password
  yourself) is kept, now titled "Add a user directly".
- New web/templates/join.html: shows which podcasts the invite is for, a
  name + password form, or "Invite link not open" for a used/expired/
  revoked/garbage token (same message for all four, so a stale link can't
  be used to probe whether it was ever valid).
- robots.txt: /join/ and /invites disallowed, same as the other account
  pages.
- Tests (invite_test.go): store-level (create/list/wrong-token/accept-as-
  editor-with-exactly-those-podcasts/single-use), expiry + revoke (an
  expired invite drops out of the open list and can't be accepted; a
  revoked one can't either), a real end-to-end HTTP test through the actual
  routes and templates (login, create invite, see it on /users with the QR
  svg and podcast name, open /join as a second client, reject a short
  password, accept, confirm the account's role+podcasts, confirm a reused
  link shows "not open"), and a QR-correctness test that compares the
  rendered SVG's dark squares pixel-for-pixel against go-qrcode's own
  Bitmap() for the same link (full QR decoding wasn't worth reimplementing
  in a test just to risk a second, independent bug there).


## 2026-10-06 – v0.37.0 – users: tiles, one page per user with rights and password
- Users page (users.html): every user is a tile (name, admin/editor tag, transcribed /
  cleaned up / last active) that opens the user's page. Sorted alphabetically (not
  case sensitive, also umlauts), explicitly in handleUsers.
- User page (user.html, admins): new panel "Rights and password": admin box, the
  podcasts the user may edit (alphabetical), new password, Save, Remove user.
  "Select all" / "Select none" (one link that switches) reloads the page with all
  boxes ticked / unticked – nothing is saved until Save, so removing one podcast from
  a user who has all of them is: Select all, untick one, Save. No JavaScript.
- Saving / adding a user now goes to the user's page (add: straight to the podcasts).

## 2026-10-06 – v0.36.1 – header: no local Setup/Queue in the menu while working on a server
- layout.html: when you are switched to a server (server pages opened through the
  local app, or the local app's own pages while a server is the active place) the
  menu no longer shows this computer's Setup and Queue. "This computer's setup" and
  "This computer's queue" are now in the place menu (the Server badge), next to
  "Manage servers…". Activity stays (it has the This computer | Server tabs).
- Setup stays in the menu when it is the page you are on, or when this computer's
  setup is not ready (the "Setup needed" dot). Without a place menu (old local
  app) the old links stay.

## 2026-10-06 – v0.36.0 – podcast page: one "Podcast settings" panel with tabs; search box in the header
- Podcast page (feed.html): the four collapsible blocks (Podcast settings, Feeds,
  People on this podcast, Spelling fixes) are now one panel "Podcast settings" with
  a small tab row: General, Feeds, People, Spelling fixes. No JavaScript: the tab
  shown is the URL #target, so the existing redirects (#feeds, #people, #spelling,
  #remove) open the right tab. Only the tabs a user may use are shown (General and
  Feeds: admins; People and Spelling fixes: everyone who can edit the podcast).
  People opens by itself while the podcast has nobody on it (as before).
- Header (layout.html): a search box on every page (desktop), submits to /search;
  it grows when focused, Alt+Shift+S focuses it (accesskey). On the search page the
  header shows the plain "Search" item, on phones the plain "Search" link.

## 2026-10-05 – v0.35.2 – fix: upload/transcription failed with "UNIQUE constraint failed: tokens.version_id, tokens.seg_idx, tokens.idx"
- trimLoops (chunks.go) gave the "[...]" marker for a cut-out repetition loop
  token index 0, which collides with the real first token of the same
  segment (also two markers in one segment). Storing the result then failed,
  locally and on the server. The tokens of every segment are now numbered
  again, in order, after trimming. Regression test in pieces_test.go.

## 2026-10-04 – v0.35.1 – podcast list always alphabetical
- Store.Feeds() (store.go) now sorts podcasts by title, case-insensitive,
  instead of by id / time added. Because every list goes through it, this
  covers the podcasts page, the search dropdown, the API (/api podcasts),
  the sitemap and `feed list` in one place. Equal titles fall back to id.

## 2026-10-04 – v0.35.0 – speaker detection on the graphics card from Setup, also on Windows
- Setup → Speaker detection has a "Graphics card" part now: with an NVIDIA
  card (driver 580 or newer) on Windows or Linux, "Use the graphics card for
  speaker detection" downloads graphics card support once (about 1.3 GB on
  Linux, 1.5 GB on Windows), puts it in place and tests it right away;
  progress shows at the top like the setup. When installed: "Remove
  graphics card support" (and "Install again" if the test failed), plus the
  "Runs on" choice as before. Other computers get a plain reason instead:
  no NVIDIA driver, driver too old, NVIDIA card too old (CUDA 13 dropped
  everything before the RTX 20 / GTX 16 series - e.g. a GT 1030 would
  otherwise have downloaded 1.3 GB for nothing; checked via nvidia-smi's
  compute capability, by name on old drivers), AMD/Intel card (no
  ready-made speaker detection for them), Mac. Transcription still uses
  those cards through Vulkan.
- New in QS-PodScript itself (gpuspeakers.go), replacing the shell code in
  install.sh: sherpa-onnx's GPU build 1.13.8 for Linux and – new – Windows,
  NVIDIA's CUDA 13 / cuDNN 9 libraries from their official PyPI packages,
  and on Windows the Visual C++ runtime the GPU build needs; every download
  pinned and checked by SHA-256. The normal libraries are saved first so
  "remove" puts them back. Libraries the running program has loaded are
  never overwritten (Linux replaces the file, Windows renames the old one
  and deletes it at the next start). Speaker detection runs in a child
  process, so it uses the graphics card right after installing – no
  restart; voiceprints computed in the app itself follow at the next start.
- If the program would no longer start with the new libraries (checked in
  a child process), everything is put back at once.
- After an update the package's normal libraries are back next to the
  program – QS-PodScript notices at start and puts the GPU build back in
  place (no download).
- Command line: qs-podscript gpu-speakers [install|remove|status] (exit code
  0 / 1 failed / 2 not possible here; the last line starts with
  GPU-SPEAKERS for scripts).
- install.sh uses it (same folders as before, so 0.34.3/0.34.4 installs are
  picked up without a new download); it now offers it with "yes" as the
  default when it finds a suitable NVIDIA card. install.ps1: same question
  plus -GpuSpeakers / -NoGpuSpeakers.
- Tested here without an NVIDIA card: full download/install of the Linux
  part, the test ends at "NVIDIA driver missing" as expected, removal
  restores the original libraries byte for byte, the re-apply after a
  simulated update works; the Windows build compiles and install.ps1 parses.
  Windows on a real NVIDIA card is untested.
- Manual, READMEs and the About page updated; docs/TECHNICAL-OVERVIEW.md
  (a short technical brief for planning) added.

## 2026-10-03 – v0.34.4 – screenshots and texts for the current state; "Unknown" in the lanes
- Found by Batz: "Unknown" (and "[crosstalk]") in the speaker lanes of an
  episode could not be clicked, so their lines could not be given to a
  person at once. They are links now like every voice: the quick menu
  ("Everything said by Unknown is actually: …") and "Check line by line".
  As they are no voice of their own (lines someone marked "Unknown / not
  speech" or "Several at once", or nobody detected), choosing a person adds
  one passage correction per line instead of a voice rename; no voice
  samples are saved from them. The menu hides the voice's own button and
  the voice-sample note for them. Test for the passage logic.
- README screenshots taken again (made-up demo podcast as before): QuickSack
  is the default look now, with the logo, drawn scroll buttons and bright
  mode. New: the podcast page, Setup (speaker detection), the Classic look
  and a phone picture; the old "QuickSack" extra pictures are gone (the
  normal ones show it now). docs/social-preview.png (GitHub's link preview)
  in the same style.
- docs/dev/screenshots.js takes all of them in one go from the demo data.
- README: looks (QuickSack default, both bright or dark, phones), speaker
  detection on the graphics card (--gpu-speakers, ~10x in a first test),
  server install in one command, link previews, patchelf for building.
- Manual: realistic times - transcription a few minutes with a graphics
  card, speaker detection on the processor up to an hour for a long
  episode, 10-20 minutes in total with --gpu-speakers.
- Linux README: speaker detection on the graphics card with the first
  measurement (RTX 3070, ~4 minutes for an episode of over an hour).

## 2026-10-03 – v0.34.3 – speaker detection on the graphics card really works
- Found by Batz: "qs-podscript gpu-check" said "GPU-CHECK OK ... cpu=133ms
  gpu=133ms" - but both ran on the processor. The speaker detection library
  shipped with QS-PodScript (sherpa-onnx) is built without graphics card
  support; it printed "Please compile with -DSHERPA_ONNX_ENABLE_GPU=ON ...
  Fallback to cpu!" and quietly ignored the NVIDIA libraries. So
  --gpu-speakers never sped anything up (the app itself noticed and stayed
  on the CPU; only the check and the installer said OK).
- install.sh --gpu-speakers now installs sherpa-onnx's own GPU build of the
  same version (1.13.8, CUDA 13 + cuDNN 9, ONNX Runtime 1.28.2; ~255 MB,
  sha256 pinned) instead of Microsoft's ONNX Runtime package - the library
  that does the speaker detection, compiled with CUDA, plus its ONNX
  Runtime. The NVIDIA libraries (CUDA 13, cuDNN 9) stay as before.
  An existing --gpu-speakers install is replaced on the next ./install.sh
  run (its version marker no longer matches); --no-gpu-speakers puts the
  processor-only libraries back.
- gpu-check runs the real test in a second process and reads everything it
  prints: a quiet fallback to the processor is now "GPU-CHECK NO-GPU", a
  crash while starting CUDA is reported instead of swallowed. The installer
  no longer says "uses your NVIDIA graphics card" in that case.
- Tested here without an NVIDIA card: the installer part downloads, checks
  and puts everything in place; with the GPU build the check gets as far as
  "libcuda.so.1 not found" (= the NVIDIA driver, the one thing only a real
  card has) and the app says "NVIDIA driver missing or too old" and uses
  the CPU; speaker detection on the CPU still works with the GPU build.
  The speed on a real card is still to be measured.
- Build: the Linux program only looks for its libraries in its own folder.
  It also had the build machine's Go module folder in its search path,
  ahead of its own folder - on a computer where that folder exists, the
  CPU-only library from there would have won. build.sh fixes the path with
  patchelf (now needed for building; CI installs it).
- A test checks that install.sh downloads the same sherpa-onnx version as
  go.mod uses.
- Installer: newer NVIDIA drivers (e.g. 615) write "CUDA UMD Version: 13.4"
  instead of "CUDA Version: 13.4" in nvidia-smi, so the installer read
  "driver supports CUDA ?" and skipped the graphics card part ("needs
  driver 580 or newer"). Both forms are read now; if neither is found, the
  driver version decides (580+ = CUDA 13).

## 2026-10-03 – v0.34.2 – drawn scroll-button icons
- Scroll buttons have drawn icons instead of text symbols (⤒ ↑ ↓ ⤓ ◉): double
  chevrons for a page up/down, double chevrons with a bar for top/bottom,
  a ring with a dot for "to the part that is playing". Small inline SVGs in
  the text colour - sharp at any size, same in both looks, CSP-safe.

## 2026-10-03 – v0.34.1 – page descriptions describe QS-PodScript, scroll bar colours
- The meta description (search results, link previews) is now the same on
  every page and says what QS-PodScript is: "QS-PodScript turns podcast
  episodes into transcripts that show who said what: speaker detection,
  voice recognition, full-text search and the audio to listen along. Free
  and open source." It no longer lists the server's podcasts, episode
  counts or the first words of an episode.
- Unchanged: titles and pictures still say which podcast / episode a link
  is about (cover art on podcast and episode pages, the banner elsewhere).
- QuickSack: the scroll buttons on the right are drawn in the opposite of
  the page again, like in Classic - a bright bar in dark mode, a dark bar in
  bright mode (they had the panel colour, so they blended in).

## 2026-10-03 – v0.34.0 – connect to a server while installing
- Installers can connect the computer to a shared server - the same as
  Setup -> "Where you work" -> "Add a server" (the Setup page is unchanged).
  When no server is saved yet they ask once (default: no): address, your
  name, password (not shown), an optional name for it, and whether to
  "Transcribe for this server". Wrong password: try again (3 times), or
  skip and do it later in Setup. Updates don't ask again; -y / -n /
  --defaults never ask.
  Linux: ./install.sh --connect ADDRESS --connect-user NAME
  [--connect-label TEXT] [--connect-work]. Windows: install.cmd -Connect
  ADDRESS -ConnectUser NAME [-ConnectLabel TEXT] [-ConnectWork].
- New command: qs-podscript connect <address> --user NAME [--label TEXT]
  [--work], and qs-podscript connect list. Asks for the password without
  showing it (or takes QSPODSCRIPT_PASSWORD). Also the way on macOS.
- The password is only used to log in once (the server gives the computer
  its own key, as before) and is never written to the install log.
- READMEs (Linux, Windows, macOS, GitHub) and the manual mention it.

## 2026-10-03 – v0.33.0 – logo, link previews, search engines
- Logo: a red rounded square with a white speech bubble saying "QSP" (the
  P in red), so it's recognisable without the name next to it. Own
  design, web/static/logo.svg; the letters are Oswald SemiBold turned
  into outlines (no font needed, SIL OFL). In the header in front of the
  name (both looks), as the browser tab icon (SVG + favicon.ico 16/32/48),
  and as the home-screen icon on phones (icon-180.png).
- Link previews (Discord, Slack, WhatsApp, Mastodon, X ...): every page has
  Open Graph and twitter:card tags. Podcast and episode links show the
  podcast's cover art, the podcast name and a description; episodes also
  quote the first words of the transcript (ads left out). Other pages show
  a 1200x630 QS-PodScript banner (web/static/og.png). Embed colour
  (theme-color) is the look's accent.
- Search engines: description and canonical address on the public pages
  (overview, podcasts, transcribed episodes, manual); everything else -
  login, search results, quiz, setup, untranscribed episodes - says
  noindex. /robots.txt keeps crawlers out of audio, API and admin pages and
  points to /sitemap.xml (overview, manual, every podcast and every
  transcribed episode). The local app tells search engines to stay away
  completely.
- Page titles: the overview is "QS-PodScript – podcast transcripts",
  episodes include the podcast ("Episode – Podcast – QS-PodScript").
- Addresses in the tags come from the address the visitor used (Host and
  X-Forwarded-Proto from the reverse proxy), so they are https://... behind
  Traefik.

## 2026-10-03 – v0.32.0 – QuickSack is the default look, now also bright
- QuickSack (the quicksack.li style) is the default look. Installs where a
  look was saved in Setup → Look keep theirs; Classic stays available.
- QuickSack can be bright as well: the same round button as in Classic
  (bottom left, on phones in the header) switches dark/bright and is
  remembered in that browser; without it the page follows the device
  setting (dark unless the device prefers light). Bright: warm off-white
  page, white panels, the same red buttons and accent, dark-gold links,
  the speaker colours of Classic's bright mode. The header bar stays black
  with the red line in both modes.
- All QuickSack colours are now colour tokens (--qs-* and the usual ones)
  with a dark and a bright set instead of fixed dark values - checked for
  contrast (text at least 4.5:1, borders at least 3:1).
- Phone header: the dark/bright button is a plain outlined icon in
  QuickSack (it took the red button style); "Log out" no longer wraps
  onto two lines.
- Setup → Look lists QuickSack first ("The default"); manual updated.
- Repository: web.go and README-linux.txt are back - the v0.31.3 commit had
  deleted them by mistake, so v0.31.3 on GitHub didn't build. .gitignore now
  ignores release zips/tarballs in the repo folder (the 0.31.2 changes zip
  had been committed along with the files).

## 2026-10-03 – v0.31.3 – faster first load (PageSpeed)
- QuickSack look: the four fonts it uses are preloaded in the page head, so
  the browser fetches them together with app.css instead of only after it
  (critical chain page -> CSS -> fonts becomes page -> CSS + fonts). The
  Classic look uses system fonts and preloads nothing.
- Font files (/static/fonts/) are cached for a year (were 5 minutes) - a
  font never changes under the same file name.
- Not changed: app.css stays a separate file ("render-blocking"). Inlining
  it would break the strict CSP and caching; after the first visit it comes
  from the browser cache (a year, versioned with ?v=).

## 2026-10-03 – v0.31.2 – phones checked, server install in one step, strict CSP possible
- Phones: checked the remaining pages at 412 px (episode, Look Who's Talking
  Now, voice page, Setup, people; logged in, logged out and the local app).
  None scrolls sideways. Fixed: transcript text ran 2 px under the floating
  scroll buttons; the "Highlight / Hide ads / Hide movie clips" row was
  squeezed to three lines per label (now wraps as whole labels); buttons
  and "Someone else…" lists are at least 34 px high on touch screens and
  narrow windows (were 26-28 px).
- install.sh --server: installs QS-PodScript as a shared server in one go -
  system service "qs-podscript-server" (instead of the menu entry and the
  per-user service), asks for the reverse proxy's address and the first
  admin login. Options --listen, --trusted-proxy, --admin <name>,
  --no-service. Listens on 127.0.0.1:8322, or 0.0.0.0:8322 when a proxy
  address is given. Running a newer install.sh again on the server (with or
  without --server) finds the service, stops it, updates and restarts it,
  keeping its listen address and proxy setting. Server uninstall.sh removes
  the service too. --help now shows the whole option list.
- Strict Content-Security-Policy possible (e.g. in Traefik): no inline
  script or style left. The light/dark script moved to /static/theme.js;
  the speaker lanes are drawn as small SVGs (positions as attributes); the
  progress bars use width classes p0-p100. A test fails if a template gets
  an inline script, style attribute, <style> block or on…= handler again.
  Recommended header in README-linux.txt (Traefik secure-headers).
- docs/dev/phonecheck.js: correct quiz addresses, modes server / public /
  local, full-page screenshots, reports buttons smaller than 28 px.
- README / README-linux.txt / manual: server install via install.sh --server.

## 2026-10-03 – v0.31.1 – phone layout: header and wide tables
- Phones: wide tables no longer push the page to the right. Helpers' work,
  account/user pages (computers, computer time, transcribed, cleaned up,
  activity), Who did what, a podcast's episode list and a person's voice
  samples show one card per row: the episode (or computer) on its own line,
  the rest below, with labels where a bare number would be unclear
  ("Busy: 16 min"). Pure CSS (class "stack", data-label on cells).
- Phone header at the top: the menu wraps onto a second line instead of
  being cut off ("ACTIV…"); "Helpers' computers" with the numbers below it
  and the button on the right, no more broken label.
- Phone header when scrolled: the status sits in its own column and gets
  "…" when short on room - it used to run over the name and "Server" badge
  (an older rule let it span the whole row).
- Checked at 412 px wide (QuickSack look, server mode): no page scrolls
  sideways any more.

## 2026-10-02 – v0.31.0 – computer time on the user pages
- Account / user page: new box "computer time" (total, of that transcribing
  and finding speakers) and a "Computer time" table per computer: episodes,
  audio, busy time, transcribing, speakers, speed (minutes of audio per
  minute of work), last job. The "Transcribed" list shows how long each
  episode took and how fast. Users page: computer time next to each user.
- Read from the step times every version already stores (download, convert,
  whisper, diarize, identify) - no manual time tracking.
- Speaker detection redone by a helper now counts for that helper and its
  computer (new: versions.speakers_by / speakers_on, schema 19). It used to
  be credited to whoever transcribed the episode. Older redone versions by
  helpers can't be told apart and are left out of the times.
- "Episodes transcribed" no longer counts redone speaker detection twice;
  shown separately ("speakers redone for N").
- Demo data: admin "demo" / "demo-password" with timings on two computers.

## 2026-10-02 – v0.30.1 – context for each line, the quick menu is back
- Voice page ("everything one voice said"): each line shows the two lines
  before and after it (greyed, with their speakers and times) and a
  "▶ Play with context" link that plays from the first to the last of them.
- Clicking a name in the lanes opens the quick "Everything said by X is
  actually:" menu again (as before 0.30.0), now with a link "Not sure?
  Check line by line →" to the voice page. Without JS the name is that link.
  The menu is narrow again for this (no text column).

## 2026-10-02 – v0.30.0 – everything one voice said
- Episode page: the names in the lanes ("Speaker 4", "Jordan Lee", …) link
  to a new page with everything that voice said in this version, line by
  line with the same name buttons as "Look Who's Talking Now": click a time
  to hear just that line (playback stops at its end), pick the right person,
  split a line by clicking a word, Save. All lines of the page count as
  checked (✓ and green edge for lines already checked); 40 lines per page,
  "Save and next page". Ads and movie clips are included, with their badge.
- "All of them are …" on that page: name every line of the voice at once
  (a person, another unnamed voice, several at once, nobody/music) or as a
  new person - a merge correction (undoable in Corrections; for an unnamed
  voice it also saves a voice sample). Replaces the old click-on-lane-name
  merge menu (no JS needed now).
- Server: only editors of the podcast see the links (same rights as the
  other corrections).

## 2026-10-01 – v0.29.0 – GitHub, macOS, Yippee-Ki-Yay
- "Look Who's Talking Now" is the normal headline (podcast page box, its
  page, episode line, stats). Only when no podcast has anything left to
  check: "Yippee-Ki-Yay… Mother-Cleaner" with a note that nothing is left
  to check or correct right now. (The sequel jokes of 0.28.0 are gone.)
- Ready for GitHub (baumbatz/qs-podscript):
  - Licence: GNU AGPL-3.0-or-later (LICENSE, official text). Help and the
    "What QS-PodScript uses" table link to the source code (AGPL: users of a
    public server can find it).
  - README.md with screenshots (docs/screenshots, made-up demo podcast -
    generator: demo_test.go, QSPS_DEMO_HOME=... go test -run TestMakeDemo),
    THIRD_PARTY_NOTICES.md + licenses/ (sherpa-onnx, ONNX Runtime,
    whisper.cpp, go-sqlite3, fonts) - also copied into every package.
  - CONTRIBUTING.md, SECURITY.md, docs/RELEASING.md, docs/SPEC.md (moved),
    issue templates, PR template, dependabot, .gitignore, .gitattributes.
  - GitHub Actions: CI (tests with/without FTS5, gofmt, Windows cross-build,
    macOS build) and Release (tag vX.Y.Z = main.go version -> Linux,
    Windows, macOS packages + source zip + SHA256SUMS, notes from DONE.md).
    The Windows Vulkan whisper-cli.exe comes from a one-time "deps" release
    (checksum-checked) instead of the git repository.
  - go.mod: go 1.24 (what it is built and tested with).
- macOS (Apple Silicon), untested on a real Mac: whisper.cpp and ffmpeg from
  Homebrew (whisper uses Metal by itself), found also when started from the
  Finder (/opt/homebrew/bin); Setup shows "Apple GPU version (Metal)";
  build-macos.sh (signed ad hoc, sherpa-onnx dylibs next to the program),
  README-macos.txt, QS-PodScript.command to double-click.
- Correction in TODO: GPU speaker detection is worth testing after all -
  on the processor it took 49 min for one episode in Batz's log.

## 2026-10-01 – v0.28.0 – Look Who's Talking, wider correction popup, defaults
- "Who's talking?" is now "Look Who's Talking" (podcast page box, episode
  line, its own page with the tagline "Who said what – checked in
  half-minute bites", user stats, manual, READMEs). When an episode is
  fully checked: "Look Who's Talking Now!"; nothing left in a podcast for
  the moment: "Look Who's Talking Too…"; after corrections: "… Look who's
  talking now!".
- Correction popup (double-click a word / click a speaker): up to 80% of the
  window wide; on wide screens two columns - who is speaking on the left,
  "Correct the text" and "Mark as" on the right - so the text field is
  visible without scrolling (popup about 280 px high instead of ~550).
  Phones: unchanged (one column).
- Default separation thresholds for the voice models without real-audio
  values: big ResNets 0.2 -> 0.3, TitaNet small 0.5 -> 0.9, ERes2Net
  0.6 -> 0.7, both CAM++ 0.5 -> 0.7 (estimates, see setup.go). Thresholds
  you saved yourself stay.
- TODO tidied: reverse proxy still open; export/import confirmed; GPU
  speaker detection moved to "possible" (limited gain).

## 2026-10-01 – v0.27.2 – Podcasts page headings
- "Podcasts" is the page title on its own line (above the search); the
  boxes below have their own, smaller headings in the same scheme: "On
  this computer" (with "Add a podcast") and "Your servers".

## 2026-10-01 – v0.27.1 – Podcasts page: two boxes
- Your computer's Podcasts page: "Podcasts" (on this computer) and "Your
  servers" are now two matching boxes; "Add a podcast" sits inside the
  first box, so it's clear that adding is for this computer. Server pages
  are unchanged. Phones: the switch button goes under the server's text.

## 2026-10-01 – v0.27.0 – one place marker, servers list, QuickSack look
- Header: the marker next to the name looks the same in every state - the
  small filled caps label the server's visitors see: "SERVER" (visitors),
  "SERVER · QSP ▾" (a server through your computer), "THIS COMPUTER ▾"
  (grey). Before: three different styles.
- Podcasts page (your computer): two matching boxes - "Podcasts" (on this
  computer, with "Add a podcast" inside the box: adding is for this
  computer) and "Your servers" (a list like the podcasts: name, address,
  logged in as, "this computer transcribes for it", Switch to it, plus
  "Manage servers"). On phones the switch button goes under the text.
- New Setup section "Look": Classic (as before) or QuickSack - the style of
  quicksack.li: near-black, red line under the header, dark red buttons,
  Oswald caps for headings, menu and buttons, Source Sans 3 for text,
  always dark (the bright/dark button is hidden). Fonts are bundled
  (web/static/fonts, SIL OFL licence files next to them) - still works
  offline.
  - Your computer's choice also applies to the server pages opened there
    (sent along in a header), so switching places doesn't change the look.
  - On a server, its admin picks the look for visitors.
- Checked in the browser: all three marker states, both looks on Podcasts,
  podcast, episode, search, Setup, server pages; phone width.

## 2026-10-01 – v0.26.2 – same menu everywhere, which computer did it
- Menu: with a server active, your own pages (Setup, Queue, Activity) now
  show the server's "Users" too (if you are an admin there) - the menu is
  the same on every page. Checked on 9 pages × 2 servers.
- Server activity log names the computer, not only the user:
  "Episode 12 (…) handed to batz on computer “office-pc”", also for
  results ("transcribed by …", "speaker detection redone by …"), rejected
  results and failures - failures now also say which episode
  ("Episode 12 (…) failed – batz on computer “office-pc”: <error>").
  The computer name is the one shown under the user's account (keys).

## 2026-10-01 – v0.26.1 – "Transcribe this podcast" on a server page does it
- Clicked on a server's podcast page (through your QS-PodScript), "Transcribe
  this podcast" now has your computer work through that podcast's waiting
  episodes, oldest first - instead of the note "This server doesn't
  transcribe itself: 873 episode(s) are waiting for the helpers".
  - Idle: starts at once. Busy: right after the current work. The podcast
    stays on the list (Queue -> "Asked for on a server") until nothing of it
    is waiting; "Take off" ends it.
  - An episode that fails on your computer is skipped for the rest of the
    run (left for other helpers) instead of being handed back to you.
  - Server: claim API takes feed_id (one podcast) and skip (episode ids);
    the server needs 0.26.1 for this - with an older one the entry is taken
    off with a note in Activity.
  - Clicked on the server directly (not through a QS-PodScript): the
    podcast goes first for all helpers, as before (message reworded).
- Tests: claim limited to one podcast (oldest first, rest stays for
  everybody), skip list. Browser: click on server page -> message, list
  entry, your computer takes the podcast's episodes one after another.

## 2026-10-01 – v0.26.0 – several servers, "where you work", Setup sections, artwork
- Several saved servers (Setup -> "Where you work"): add, rename, remove,
  "Transcribe for this server" per server. An existing connection moves into
  the list by itself (keeps its key and its tick).
- Where you work = this computer or one server, switched in Setup, on the
  Podcasts page ("Your servers") or with the place menu next to the name at
  the top (no JS needed; a few lines close it when clicking elsewhere).
  - Server active: Podcasts, Search and People are the server's (one fixed
    local address that always shows the active server); this computer's
    podcast pages (also old links/bookmarks) lead there too.
  - Setup, Queue and Activity always stay this computer's; Setup and
    Activity get a "Server: <name>" tab for the active server (admins).
  - The "Shared server – back to your own" bar is gone (still shown when an
    older local app opens the server). The place menu shows the server's
    name in blue, plus "Logged in as …".
  - The server learns the place menu from the local app (header
    X-QSPodScript-Places) - both sides need 0.26.0 for it.
- Search: only where you work (your choice) - the combined search of 0.25.1
  is gone; the API stays.
- "Start transcribing": own queue first, then the ticked servers take turns
  (one episode each in rotation; a server that is down is skipped).
- "Asked for" list remembers the server of each entry; queue page says which
  one; removing a server drops its entries.
- Setup restructured: section buttons at the top (Where you work, Programs
  and models, Transcription, Speaker detection, Moving), sections separated.
- Podcast artwork: the image the feed names (itunes:image, else image/url)
  is downloaded once, shrunk to 300 px JPEG (stdlib only, transparency on
  white) and kept in data/images; served from /img/ with a long cache (the
  file name changes with the picture). Shown on the Podcasts list, podcast
  page, episode page (small, next to the podcast link) and search results.
  Checked at start, after podcast changes and weekly. Schema v18.
- Tests: saved servers (moving the old connection, places, removal, menu
  header), servers taking turns + one down, asked-for list per server, feed
  image parsing, shrinking. Browser: two servers, switching both ways from
  local and server pages, redirects, tabs, artwork on desktop and phone.

## 2026-10-01 – v0.25.1 – search your podcasts and the server's together
- The search on a local QS-PodScript that is connected to a server now
  searches both and shows one list (before: only its own podcasts - with
  none of its own, it never found anything).
  - Server: new GET /api/v1/search (same as the public search page, plus
    the podcast and person lists for the menus).
  - Local: asks both (server: 5 s at most), merges by best match (FTS5
    score) or date, then cuts the page; up to 300 results per source
    (10 pages).
  - Each result says "This computer" or "Server" when you have podcasts in
    both places. Server results open through the pass-through (editable,
    words highlighted).
  - Podcast / "Said by" menus list both, grouped "On this computer" / "On
    the server"; picking one searches only that side.
  - Server not reachable: a notice, your own results still show. Server
    older than 0.25.1: a notice to update it.
  - "Search" in the menu of server pages opened through your QS-PodScript
    leads to the combined search (like Setup).
- The brand badge in the header doesn't wrap any more.
- Tests: merge order, menu values; browser: combined, each side filtered,
  server down, local without podcasts, server result opens with highlights.

## 2026-10-01 – v0.25.0 – search, cleaner pages for visitors
- Search (menu "Search", plus a box on the Podcasts page), on the server and
  locally, for everybody (no login needed):
  - Searches what the episode pages show: corrections, speaker names and
    spelling fixes included, ads left out, movie clips marked "Movie clip".
  - All words must appear in the same passage (~30 words, cut at sentence
    ends); "exact phrase"; word* for beginnings; capitals and accents
    (ä/é/ß…) don't matter; apostrophes as in the word index ("don't").
    Anything else typed is ignored, so no input can break the query.
  - Filters: podcast, "Said by" (a person), leave out movie clips; order by
    best match / newest / oldest; 30 per page with Previous/Next. No JS.
  - A result opens the episode at that spot (#t…) with the found words
    highlighted (?hl=…, marked on the server side).
  - Index: SQLite FTS5 (build.sh now builds with -tags sqlite_fts5, Linux
    and Windows). Without FTS5 (plain `go build`) it falls back to a normal
    table with LIKE - same results, no ranking.
  - Kept up to date in the background: every episode has a fingerprint (main
    version, its corrections, spelling fixes, people's names); any change
    (any POST, except helpers' progress reports) makes the indexer look
    within ~2 s, and it checks every 5 minutes anyway. The first build after
    the update runs in the background; the search page says while it is
    building. Removed podcasts/episodes disappear from the index.
  - New index on corrections(version_id) - also makes episode pages faster.
- Visitors on the server (not logged in, or no edit rights for a podcast):
  - Podcast page: no feed address, no queue states; tabs "Transcribed" (the
    default) and "All" (not yet transcribed ones say so); "N of M episodes
    transcribed".
  - Episode page: no model/threshold details, versions, "waiting for a
    helper" notes, recognition notes, corrections list or "highlight what
    nobody has checked" (that's for editors).
  - Help: a short visitor guide (reading, listening, search, how to help)
    instead of the program manual.
  - Phones: "Log in" sits next to the name instead of its own row.
- Manual: "Searching all transcripts" in section 3.
- Tests: search in both modes (FTS5 / plain): phrases, prefixes, accents,
  ads left out, query syntax can't get through, spelling fix re-indexes,
  deleted episodes vanish, highlighting. Browser at 1280px and 390px.

## 2026-10-01 – v0.24.0 – Setup: "This computer" / "Server" tabs
- Setup has two tabs when your computer is connected to a server where you
  are an admin: "This computer" (local settings) and "Server" (the shared
  server's settings, opened through your QS-PodScript). Each tab says in one
  line what it covers and that server episodes use the server's speaker
  detection settings. Plain links, no JS.
- "Server setup" removed from the Users page.
- Where am I: the name at the top left says "QS-PodScript SERVER" on server
  pages and "QS-PodScript THIS COMPUTER" on your own pages (only shown while
  connected to a server).
- Your computer now knows whether you are an admin on the server (stored at
  connect, refreshed with the server's podcast list) - non-admins don't get
  the Server tab. Connections made before 0.24.0: shown until the server
  answers otherwise (the server checks rights anyway).
- Help: section 13 describes the tabs and the badge.
- Tested at 1280px and 390px: both tabs switch back and forth.

## 2026-09-30 – v0.23.0 – clicks always end up in a queue
- "Transcribe again" / "Redo speaker detection" while your computer is busy:
  - On a server page (opened through your QS-PodScript): the episode goes on
    your computer's list ("asked for") and is done right after the current
    work – no more "waits for the helpers … click again once idle".
  - On your own pages: "Transcribe again" was already queued (first);
    "Redo speaker detection" was refused ("Something else is running") and
    is now queued too (speaker detection only, from the saved audio copy).
- The list is done after any job that ends by itself (queue run, helper job,
  single episode), and first when you press "Start transcribing". "Stop"
  keeps it. It survives a restart; disconnecting from the server clears the
  server entries.
- Queue page: new block "Asked for on the server" with "Take off" (the
  episode then stays in line for the other helpers).
- If the server can't be reached, the entry stays and is tried next time;
  if another computer took the episode meanwhile, it is skipped.
- Tested: local busy with a speaker detection, clicked "Redo speaker
  detection" and "Transcribe again" on server pages -> both listed, done
  automatically right after the local job, results arrived on the server.
- Update both the server and your computers (the message on the server page
  comes from the server).

## 2026-09-30 – v0.22.2 – phones, version check
- Phone layout (up to 760px wide), checked at 390px:
  - Header: name + a small light/dark button in the first row, the menu as
    one line that scrolls sideways (no more cut-off "Activity"), status and
    Start/Stop in one row, smaller buttons. When scrolled: name, status and
    theme button in one line, the menu below.
  - The floating light/dark button is hidden on phones (it is in the header
    now), so the pages use the full width (16px margin left; only the scroll
    buttons keep a strip on the right).
  - Queue: podcast and date move under the episode title, the buttons become
    icons (⤒ ⤓ ✕, labels stay for screen readers) – nothing is cut off.
  - Episode buttons and the "Shared server" bar are smaller.
- Version check between your computer and the server:
  - The server sends its version with every answer; your QS-PodScript
    remembers it (API calls and pages opened through it).
  - Pages opened through your QS-PodScript send its version to the server.
  - If they differ, a yellow bar says so and which one to update: on your
    own pages (e.g. Podcasts) and on the server pages. Servers older than
    0.22.2 don't send their version, so no bar is shown for them.
  - Tested: server 0.22.2 + local 0.22.1 -> both bars show up, on desktop and phone.

## 2026-09-29 – v0.22.1 – drag to reorder the queue
- Queue page: hold the handle ⠿ of an episode, move it, let go - the numbers
  follow while dragging, the new order is saved at once (fetch; on error the
  page reloads with the order the server really has). Mouse, pen and touch
  (pointer events). Without JS the buttons To the top / To the end remain.
- Saving: the listed episodes get descending priorities above everything
  that isn't listed, so exactly the shown order is kept (/queue/order).
- Priorities are relative now: "Transcribe again"/redo, "To the top" and
  "Transcribe this podcast next" put episodes above everything waiting
  (before: fixed 100 / 50, which a manual order could outrank). The podcast
  put first is remembered (setting first_feed) for its "✓ Goes first" mark.
- Numbering was and is computed on every page load (always 1..n).
- Tested in the browser: dragged #5 to #2 -> numbers updated live, order
  kept after reload; "podcast first" and undo still work.

## 2026-09-29 – v0.22.0 – the queue
- New page "Queue" (menu, admins): the waiting episodes in the order they
  are taken (priority, then oldest first) with number, podcast, date and
  notes ("transcribe again", "speaker detection only", "moved up / asked
  for", "podcast first", "moved to the end"); first 200, "Show all"; the
  current episode on top. No JS.
- Per episode: To the top (priority above all others, >= 100), To the end
  (below all others), Take out.
- Take out: a never-transcribed episode gets the new status "skipped"
  (shown as "Not queued", own tab on the podcast page, never taken by the
  local queue or by helpers, not reset by feed refreshes); an episode queued
  for "transcribe again"/redo goes back to done (keeps its version).
  "Transcribe now" or "Put all back" (queue page / podcast tab) returns them.
- Podcast page: "Don't transcribe" next to each waiting episode, "Queue →"
  link next to the tabs.
- Tested on 400 episodes: to the top -> #1, to the end -> #399, take out ->
  "Not queued 1", put back -> first again (by date).

## 2026-09-29 – v0.21.0 – remove a podcast, one podcast first
- Podcast settings -> "Remove this podcast" (admins): explains what goes
  (episodes, transcripts, versions, corrections, checked passages, audio
  copies, spelling fixes only for this podcast) and what stays (people,
  voice samples); needs a ticked confirmation box; refused while one of its
  episodes is being transcribed. Tested: 403 episodes with versions, tokens,
  corrections, checks and audio removed; people and samples kept.
- "Transcribe this podcast next" on the podcast page: its waiting episodes
  get priority 50 (episodes asked for one by one keep 100) - a running queue
  takes them right after the current episode; if nothing runs, transcribing
  starts (this podcast first, then the others). Only one podcast goes first
  at a time; "✓ Goes first · normal order" undoes it. On a server without
  its own transcription: first in line for the helpers.
- TitaNet large: default separation threshold 0.96 (Batz, real episode with
  4 hosts + movie clips: 0.55 gave 127 voices). The synthetic test had
  suggested 0.55 - real audio needs far higher values for TitaNet.
- Server pages in the local app: one "Setup" (your own computer's); the
  server's settings via Users -> "Server setup".

## 2026-09-29 – v0.20.0 – helpers work only when asked
- Batz: clients must not start transcribing/diarizing by themselves - the
  user decides whether to run through waiting episodes or do specific ones.
- Removed the automatic "ask the server every minute" (0.19.1).
- Clicking "Transcribe again" / "Redo speaker detection" on a server page
  opened in your QS-PodScript: your computer takes exactly that episode
  (claim API: optional episode_id) right away if it is idle, does only that
  one and stops. The server's message says so ("Your computer … now") - or,
  if your computer is busy, that it waits for the helpers.
- Running through the server's waiting episodes: only with "Start
  transcribing" and "Transcribe for the server" ticked (own podcasts first).
- Tested: episode queued directly on the server stayed waiting for 70 s
  (nothing automatic); a click in the local app took exactly the clicked
  episode although an older one was waiting first, and didn't continue.
- "Highlight what nobody has checked yet" is on by default (a choice made
  in the browser is still remembered).
- Texts: Setup checkbox, server messages, Helpers' work page, manual.

## 2026-09-29 – v0.19.5 – helpers survive a server that's briefly away
- Batz saw "could not report progress to the server ... connection attempt
  failed". That ping only extends the reservation (hours long), so one
  failure is harmless - but if the server was unreachable at the END, the
  finished work was lost ("upload: ...") and the episode had to be redone.
- The upload is now retried for about an hour (30 s, 1, 2, 5, 10, 10, 15,
  15 min) while the server can't be reached; the top bar says "Server not
  reachable – trying again in …". It stops at once if the server refuses the
  result itself (e.g. reservation handed back). Tested: server stopped
  during the job, started again 45 s later -> uploaded on the 3rd try.
- After a failed "still working" ping the next one comes after 1 minute
  instead of 10; the log line is a note, not a warning.

## 2026-09-29 – v0.19.4 – "Stop now" stops at once
- Speaker detection is C code (sherpa-onnx) that can't be interrupted - its
  progress callback's return value is ignored - so "Stop now" waited until
  it had finished and then threw the result away (minutes on the CPU).
- It now runs in a child process ("qs-podscript diarize-file <wav> <out>
  <request>", internal): "Stop now" ends it immediately. Tested with an
  85-min audio: stopped during "Detecting speakers" -> idle after 0.2 s,
  no process left. Normal runs unchanged (same results, local redo tested).
- Bonus: if speaker detection crashes or fails on the graphics card, the app
  keeps running and does that episode on the CPU (and stays on the CPU for
  the rest of this run).
- Helper processes (gpu-check, diarize-file) never apply a pending import.

## 2026-09-29 – v0.19.3 – your computer's status on server pages
- Server pages opened through the local app show "Your computer: …" at the
  top right - the local job (state, step, progress, "[server] <episode>"
  linking to that episode on the server) with the local Start/Stop buttons,
  instead of the server's status. The pass-through answers /_local/events
  and /_local/queue/start|stop itself (only these three) from the local app,
  so no cross-site calls are needed.
- An episode page that waits for a helper reloads by itself when your
  computer finished (tested: redo clicked -> new version shown after 16 s
  without touching anything).
- Menu entries don't wrap ("My setup", "Server setup").

## 2026-09-29 – v0.19.2 – see what waits for the helpers
- "Reserved episodes" is now "Helpers' work": besides what helpers are
  working on it lists what is waiting, in the order helpers get it, with
  the kind of work (transcription / speaker detection only) and "asked for"
  for episodes someone queued by hand. Before, a queued episode appeared
  nowhere, so it looked as if nothing had happened.
- Server pages are marked clearly: "QS-PodScript SERVER" in the header;
  opened through the local app the bar says "Shared server – you are logged
  in as <name>. Everything on these pages happens on the server."

## 2026-09-29 – v0.19.1 – helpers take work by themselves
- Batz: on the client PC, "Transcribe again" / "Redo speaker detection" on
  server pages only showed "Next up" - the work waited until someone pressed
  "Start transcribing" on a helper PC.
- Now a local app with "Transcribe for the server" ticked takes server work
  by itself while it is idle: it asks every minute, and right away (1 s)
  after a click on "Transcribe again" / "Redo speaker detection" /
  "Start transcribing" on a server page opened through it. It then keeps
  taking server work until there is none (never starts your own queue).
  If it became busy between asking and starting, the job is given back.
  Tested: click on the server page -> helper took it without pressing
  anything -> version 10 on the server 20 s later.
- Server pages opened through the local app: the menu shows "My setup"
  (your own computer) and "Server setup" (the shared server) instead of one
  "Setup" that led to the server.
- Texts: Setup checkbox, server messages, manual.

## 2026-09-29 – v0.19.0 – the server leaves the work to the helpers
- Problem (Batz): on server pages opened through the local app, "Redo
  speaker detection" / "Transcribe again" ran on the server's own worker (one
  job at a time, on the container's CPU) - a second PC's request waited "next
  in line"; the PCs' graphics cards weren't used.
- Server default now: it doesn't transcribe itself (Setup -> "Who
  transcribes", setting server_transcribes; on = old behaviour).
  - "Transcribe again" queues the episode for the helpers (first in line).
  - "Redo speaker detection" queues a new kind of job: a helper downloads the
    server's audio copy (/audio/...), runs only speaker detection (its own
    GPU if it has one) and uploads turns + voiceprints (no audio). The server
    builds the new version from the old transcript like a local redo: voice
    merge, corrections and checked passages carried over, recognition.
    Schema v17: episodes.job_kind / job_source. Only workers >= 0.19.0 get
    these jobs; the server's own queue never takes them.
  - "Start transcribing" / "Transcribe this podcast" start nothing on the
    server; they report how many episodes wait for helpers (a podcast started
    this way goes first).
  - Top bar on the server: "Helpers' computers: N working · M waiting" with
    a link to Reserved episodes (the job status shows while the server itself
    runs something, e.g. Identify speakers).
  - Episode page: "A new speaker detection / transcription is waiting for
    (being done on) a helper's computer".
- Failed helper jobs are retried by another computer (up to 3 attempts)
  instead of being dropped for already transcribed episodes; an expired or
  handed-back reservation of such an episode goes back into the queue.
- Tested end to end: server + connected local app: redo queued on the
  server -> picked up by the helper -> "speaker detection only", 2 voices in
  9 s -> version 9 on the server with transcript from v4, 4 corrections
  carried over, recognition run. Unit test for the job flow (old worker gets
  nothing, failure -> queued again, result -> main version).
- Server's Setup page: title "Setup of the server"; opened through the
  local app it says so and links to your own Setup page; it no longer shows
  "Connect to a QS-PodScript server" (that is only for local apps).
- Setup page: model size shown for every voice model.
- Manual (server section: who does the work; server Setup via local app;
  import on a server) and both READMEs (server work, import, Windows:
  export/import, "Who's talking?", connecting) updated.

## 2026-09-29 – v0.18.1 – faster pages
- Measured locally with a 90-min episode (12,500 words) and 400 episodes:
  episode page 150-210 ms on the server side, 846 KB of HTML; podcast page
  18 ms. Nothing slow locally - the cost is in transfer and in the database
  under concurrent requests (server in an LXC, through Traefik / the
  pass-through):
- Pages, CSS and JS are sent gzip-compressed (episode page 846 KB -> 131 KB).
  Not audio, downloads, the live event stream or the worker API.
- CSS/JS linked with ?v=<version> and cached by the browser for a year
  (before: downloaded again on every page, no cache headers at all).
- Database: up to 8 connections instead of 1 - pages no longer wait for each
  other or for a running job; write transactions lock at once
  (_txlock=immediate), busy timeout 10 s, synchronous=NORMAL (WAL: no disk
  flush per write - slow on ZFS/network storage).
- Fewer writes per request: a connected computer's "last seen" is written at
  most once a minute (before: on every request through the pass-through,
  i.e. several times per page); the checked-progress numbers are only
  written when they changed (before: on every episode page view).
- The local app's start page asks the server for its podcast list at most
  every 30 s.

## 2026-09-29 – v0.18.0 – people in an episode
- Before: no per-episode list - every person with voice samples was a
  candidate in every episode (people not on the podcast's list only needed
  +0.05 similarity).
- New panel "People in this episode" on the episode page (editors): presets
  "Only the hosts", "Hosts and regulars", "Everybody (no limit)", or tick
  people. Schema v16: episode_people + episodes.people_limited (an empty
  list = nobody known, only unnamed voices).
- Recognition (automatic after transcription, "Identify speakers", worker
  results on the server) only considers those people when a list is set;
  saving runs recognition again at once. Manual corrections are kept.
  Tested: episode with Brian + Ally recognized -> "Only the hosts" ->
  only Brian recognized.
- The one-click speaker buttons (correction menu, "Who's talking?") show
  the episode's people when set, else the podcast's hosts and regulars.
- Activity log: "set who is in an episode". Manual updated.

## 2026-09-29 – v0.17.2 – split lines in "Who's talking?", speaker buttons everywhere
- "Who's talking?": clicking a word (not the first) splits the line there;
  both parts keep the previous choice and get their own buttons, so a
  speaker change in the middle of a sentence no longer has to become
  "Several at once". Field names are renumbered in the browser; the server
  already took any number of rows (tested: 4 -> 5 rows, correction 134.5-145.3 s).
  Needs JS; without JS lines stay whole.
- Episode page correction menu: one-click speaker buttons like in the quiz
  (hosts + regulars of the podcast, the episode's unnamed voices, several at
  once, unknown); "Someone else…" list for everybody else / new person / new
  voice ("Assign" button appears once something is picked there). Works
  without JS too (buttons submit directly).
- The menu is wider (28rem) and, if it fits neither below nor above the
  word, opens on the bigger side and scrolls.

## 2026-09-29 – v0.17.1 – checked passages carry over and are visible
- Carrying over to a new version (Transcribe again / Redo speaker
  detection / worker result for the same audio) now also runs when the old
  version only has checked passages (before: only with manual corrections -
  pure confirmations were lost).
- Checked passages are copied to the new version (schema v15:
  checks.carried, not counted again in anyone's numbers) - except the parts
  spoken by unnamed voices: those can't be carried, so they must be checked
  again. Tested with Redo speaker detection: 133.3-170.3 s checked ->
  133.3-156.7 s carried (the tail was an unnamed voice).
- Fix: carried corrections no longer count as "checked by a person" (they
  cover the whole old timeline, would have made everything look checked).
- Episode page: green ✓ (◐ = partly) before the speaker name of paragraphs
  a person confirmed; green "Checked" lane above the transcript; switch
  "Highlight what nobody has checked yet" (tints unconfirmed paragraphs,
  pure CSS, remembered in the browser).

## 2026-09-29 – v0.17.0 – "Who's talking?": checking speakers in small pieces
- New page per podcast (/feeds/{id}/quiz, block "Who's talking?" on the
  podcast page) and per episode (/episodes/{id}/quiz, "Check in small
  pieces" + "N% of this episode checked" on the episode page).
- A passage is ~25-40 s of the main version, ending at a speaker change if
  possible (never across > 5 s of silence/music; ads and clips left out),
  cut into rows at sentence ends (2.5-12 s). The audio plays only that
  passage (media fragment #t=from,to - works without JS); JS adds "Play
  again", click a line to hear it, current line/word highlighted.
- Per row: the current speaker is ticked; chips for the podcast's hosts and
  regulars (colours as in the transcript), "Several at once", "Nobody /
  music", and "Someone else…" (all other people, or a new voice). Pure
  HTML form (radio buttons styled as chips).
- Saving: changed rows become normal range corrections (neighbouring rows
  with the same new speaker = one correction; naming a person saves a voice
  sample as usual; undoable on the episode page), the passage is recorded as
  checked (schema v14: table checks, versions.quiz_total_ms/quiz_done_ms).
- Which passage: unchecked ones (< 80% covered by checks or manual speaker
  corrections) of a random unfinished episode; unnamed voices first, then
  many speaker changes / crosstalk / unknown; random among the top ones.
  In-memory holds: 10 min for the one shown, 30 min for skipped ones - two
  helpers don't get the same passage.
- Checked lines get a ✓ on the episode page; podcast page shows passages
  checked, helpers, episodes started/finished; account page shows your count;
  activity log "checked a passage". Rights: editors of the podcast (server),
  everyone locally; works through the pass-through too.
- Manual: "Who's talking? – checking in small pieces".
- Not yet: checks are per version - after "Transcribe again" the new version
  starts unchecked (corrections are carried over, checks not yet).

## 2026-09-28 – v0.16.0 – more voice models, speaker detection on the graphics card
- Voice models for speaker detection: 9 instead of 3 (all from the
  sherpa-onnx model release, downloaded when first used): ResNet34 (default),
  ResNet152/221/293 (WeSpeaker), TitaNet small/large (NeMo), ERes2Net and
  CAM++ (3D-Speaker), CAM++ (WeSpeaker).
- Separation threshold per model (the models measure distance on different
  scales; one shared value made the big ResNets put everyone into 1-2
  voices). ResNet34 keeps the old setting name, so existing installs keep
  their value. Defaults from a sweep on the synthetic 4-voice test podcast
  (322 s, TTS voices, share of time in the wrong voice):
    resnet34 0.5: 4 voices 19.5% | titanet-large 0.6: 4 voices 13.8%, 0.5: 10 / 14.2%
    eres2net 0.6: 7 / 14.6%, 0.7: 5 / 14.2% | titanet 0.5: 6 / 16.1%
    resnet152 0.2: 14 / 16.6% | resnet221 0.2: 7 / 31.9% | resnet293 0.2: 9 / 28.1%
    campplus 0.5: 17 / 43.5% | campplus-3d 0.5: 16 / 47.4%
  -> defaults 0.5 / 0.55 / 0.6 / 0.5 / 0.2 / 0.2 / 0.2 / 0.5 / 0.5.
  Synthetic voices: a hint, not a verdict - real episodes decide.
  CPU time relative to ResNet34: 152 2.9x, 221 4.0x, 293 5.1x, TitaNet large
  1.2x, ERes2Net 1.15x, TitaNet small 0.6x, CAM++ 0.62-0.68x.
- Setup page: table of models with time and own threshold field (no JS),
  "Runs on" (graphics card when it works / always CPU) with the current state.
- Speaker detection on the NVIDIA graphics card (Linux, optional):
  install.sh --gpu-speakers downloads the GPU build of the same ONNX Runtime
  version the package ships (1.28.2, CUDA 13) plus CUDA runtime, cuBLAS,
  cuRAND, NVRTC and cuDNN 9 from NVIDIA's PyPI wheels (pinned versions,
  sha256 checked; skipped if the system already has CUDA 13 + cuDNN 9) into
  <install>/cuda; re-applied on updates; --no-gpu-speakers removes it.
  Needs driver >= 580. The app restarts itself once with that folder in
  LD_LIBRARY_PATH.
- Safety: a failing CUDA start aborts the whole process inside ONNX Runtime
  (measured here), so the app tests it in a child process
  ("qs-podscript gpu-check": same voiceprint on CPU and GPU, must match)
  and only then uses the graphics card; otherwise CPU with the reason shown.
  A marker file is kept while GPU work runs - if the app died during it, the
  next start stays on the CPU until the speaker settings are saved again.
  If creating the detector on the GPU fails, that episode uses the CPU.
- New command: qs-podscript speaker-bench <audio> [models] [--cpu]
  [--threshold=a,b]: speaker detection only, prints time and voices.
- Manual page lists where speaker detection runs; config: speaker_device,
  diarize_threshold now applies to the current model.
- Not tested on a real NVIDIA card here (no GPU in the build machine):
  tested the CPU path with the GPU ONNX Runtime, the restart with the
  library path, and the check failing cleanly without a driver.

## 2026-09-28 – v0.15.1 – reverse proxy on another machine
- server --trusted-proxy <ip|cidr,...>: X-Forwarded-For is believed only from
  loopback and these addresses (before: loopback only - with Traefik in
  another LXC every visitor had the proxy's IP and all shared one
  login-attempt limit). Uses the entry the proxy added (last), not one a
  client could send.
- README-linux: Traefik example, upload timeout hint, systemd system unit,
  install options for a server.

## 2026-09-28 – v0.15.0 – reserved episodes overview, anonymous public names
- Schema v13: users.display_name / show_name, episodes.lease_device /
  lease_since.
- Public names: everybody (also without login) now sees "Transcribed by /
  Cleaned up by" on episodes. Users appear as "Volunteer <number>" unless
  they opt in on their account page ("How others see you": public name,
  empty = login name, checkbox "Show this name publicly"). Admins always see
  login names.
- Admin page "Reserved episodes" (/work, linked from Users): reserved
  episodes with helper, computer, since, runs out, earlier failures;
  "Hand back" requeues at once (not counted as a failure; the old lease's
  upload is refused). Episodes that failed on workers: "Try again" (resets
  the failure count). Both recorded in the activity log.
- Manual: public names, reserved episodes.

## 2026-09-28 – v0.14.0 – homeserver: users and who did what
- Schema v12: corrections.user_id, versions.transcribed_by / transcribed_on
  (user + computer name of the worker), table user_activity.
- Recorded (server mode): logins, connected computers, transcribed episodes,
  failed/rejected worker results, every successful edit (corrections with
  kind, undo, undo carried, identify, voice merge, spelling fixes, people of a
  podcast - generic in the rights check: only when the handler succeeded),
  password changes, removed keys, user management.
- Account page (click your name): change own password (current + new twice;
  other browsers logged out, this one stays), connected computers with Remove,
  own record. Admins: per-user record page (numbers, transcribed episodes with
  computer, cleaned-up episodes with number of changes, computers, recent
  activity), numbers in the Users list, "Who did what" (last 300 actions).
- Episode page (logged-in users): "Transcribed by X · Cleaned up by Y (n)".
- Manual: your account.

## 2026-09-28 – v0.13.0 – homeserver, step 2: helping from your own app
- Server API (remote.go, schema v11: api_tokens, lease columns on episodes):
  POST /api/v1/login (name + password -> token for that computer),
  GET /api/v1/podcasts, POST /api/v1/work/claim | {lease}/progress |
  {lease}/fail | {lease}/result (multipart: result JSON + 24 kbit audio).
  Leases: 3 h, +2 h per progress report, expired ones go back into the queue,
  3 failed attempts -> "Failed". Workers below 0.13.0 are refused. Status
  "Being transcribed by a helper" while reserved.
- Server stores a result like a local transcription: garbage check, new
  version, audio copy from the worker (timestamps fit), voice merge,
  carry-over of corrections, identification with the server's voiceprints.
- Pipeline: speaker detection + transcription moved into analyzeAudio(),
  shared by local episodes and server jobs.
- Local app (remoteclient.go): Setup "Connect to a QS-PodScript server" (url,
  name, password; only the token is stored), "Transcribe for the server"
  (queue continues with server jobs when the own queue is empty, progress
  pings, failure reported back), Podcasts page lists the server's podcasts.
- Pass-through: the server's pages on a second local port (8323) with the
  token added - same workflow as local podcasts, changes go straight to the
  server; only POSTs from its own origin are forwarded; blue bar "You are
  working on the shared server - back to your own QS-PodScript".
- Manual: helping from your own QS-PodScript. Tests: remote_test.go.

## 2026-09-27 – v0.12.0 – homeserver, step 1
- `qs-podscript server [--listen 127.0.0.1:8322]`: the same app as a shared
  server (design in SPEC.md). Public, without login: podcast list, podcast and
  episode pages, transcript download, audio, manual. Logged-in editors: fix
  speakers/text/marks, identify / merge voices, people of a podcast, spelling
  fixes - only for podcasts assigned to them. Admins: everything else
  (podcasts, feeds, transcribing, setup, people pages' rename/remove,
  export/import, activity, users). Checked in the handlers (guard per route,
  podcast derived from /feeds/{id}, /episodes/{id}, /versions/{vid}) and hidden
  in the pages. Local mode unchanged (you are the admin).
- Schema v10: users (PBKDF2-SHA256, 600k rounds, stdlib), user_podcasts,
  sessions (random token, only its hash stored, 30 days, HttpOnly, SameSite=Lax,
  Secure behind HTTPS). Login rate limit: 8 failures per address in 15 min.
  Password change logs the user out everywhere.
- Users page (admins): add, admin flag, podcasts per editor, new password,
  remove. CLI: `qs-podscript user list|add <name> [--admin]|passwd|admin|delete`.
- Same-origin check accepts https (reverse proxy) as well as http.
- README-linux: server setup + nginx example; manual section 13.
- Tests: auth_test.go.

## 2026-09-27 – v0.11.0
- Export / import for moving to another computer (transfer.go). Setup page,
  "Moving to another computer": Export everything = one zip with a manifest,
  the database (consistent copy via VACUUM INTO, works while running) and the
  audio folder (stored uncompressed). Import = upload (streamed to disk),
  checked (is an export, not from a newer schema, database opens, only plain
  audio file names) and unpacked to data/import-staging; applied at the next
  start before the database opens: current database + audio are moved to
  data/before-import-<time>, the imported ones take their place. This
  computer's settings for graphics card / engine / model are kept. Pending
  import shown on the Setup page with "Cancel the import".
- CLI: `qs-podscript export <file.zip>`, `qs-podscript import <file.zip>`
  (refuses while QS-PodScript is running).
- Manual: moving to another computer / backup.
- Tests: transfer_test.go (round trip, machine settings kept, backup made,
  garbage rejected).

## 2026-09-27 – v0.10.0
- Several feeds per podcast (schema v9): table `feed_sources`; a podcast (table
  `feeds`, keeps settings, people, spelling fixes) has its main feed plus any
  archive feeds; episodes remember which feed brought them (`source_id`).
  Migration turns every existing podcast into one with one feed.
- One episode list and one oldest-first queue per podcast. Duplicates across
  feeds are stored once: same guid, else same audio file name (without query,
  ignoring very short/generic names), else same title within a day. Only the
  item with the same guid from the feed that brought the episode updates it.
- Podcast page: "Feeds" panel (title, address, episode count, last error,
  Remove) and "Add another feed"; episode list shows the feed name when there
  is more than one. "Check for new episodes" / "Start transcribing" read all
  feeds; a failing feed doesn't stop the others. Removing a feed removes its
  never-transcribed episodes, keeps transcribed ones, never the last feed; if
  the main feed is removed another becomes main.
- TODO.md cleaned up to the list Batz kept; manual updated.
- Tests: sources_test.go.

## 2026-09-27 – v0.9.3 – renamed to QS-PodScript
- New name everywhere: pages, window, manual, READMEs, installers, menu entries
  (Start menu / app menu "QS-PodScript"), program file `qs-podscript(.exe)`,
  packages `qs-podscript-<version>-…`, Go module, systemd user service
  `qs-podscript.service`, install folders `%LOCALAPPDATA%\qs-podscript` and
  `~/.local/share/qs-podscript`, env vars QSPODSCRIPT_HOME / QSPODSCRIPT_DIR /
  QSPODSCRIPT_URL (the old PODSCRIBE_* names still work).
- Migration: the installers move an existing podscribe installation's data
  folder to the new place (transcripts, people, voice samples, models, GPU
  build), stop the old program/service, remove old shortcuts, menu entry,
  command and service, and keep autostart on if the old install had it. On
  first start the app renames data/podscribe.db (+ -wal/-shm) and the log to
  qs-podscript.*. Remembered browser settings (theme, speed, hide ads/clips)
  are read from the old keys too.
- "People on this podcast" heading without the count.
- Entries below this one still use the old name.

## 2026-09-27 – v0.9.2
- Podcast page, "People on this podcast": three lists – Hosts, Regulars, Not on
  this podcast – each alphabetical with a count. Choosing Host / Regular / Not
  on this podcast saves at once (POST /feeds/{id}/roster/{pid}, JSON) and moves
  the person into the right list with a short "saved" highlight; on error the
  radio goes back and says why. Without JS the whole form still saves with
  "Save people" (which also adds new names).

## 2026-09-27 – v0.9.1
- Person page: Settings (rename, remove) above the voice samples.
- Removing a person is explained in place instead of a short browser popup:
  a red "Remove <name>…" panel lists what happens (samples deleted with their
  count, taken off all podcasts, N hand-assigned passages in M episodes show
  "Removed person", same name later = new person) and points to Rename /
  removing single samples as alternatives. Needs a ticked "I understand" box;
  the server checks it too. No JS.
- Manual: renaming and removing people.

## 2026-09-27 – v0.9.0
- Manual section 12 "What podscribe uses": every tool and model with what it
  does, a link to the project and the version actually in use on this computer
  (podscribe + OS/arch + Go version, whisper.cpp release + CPU/CUDA/Vulkan,
  speech model file, FFmpeg version asked from ffmpeg itself (cached),
  sherpa-onnx + ONNX Runtime versions from the library, detection and
  voiceprint models, Silero VAD, SQLite). about.go.

## 2026-09-27 – v0.8.9
- Scroll buttons on the right use the same contrasting style as the theme
  button: bright panel with dark arrows in dark mode, dark panel with light
  arrows in bright mode (background var(--ink), arrows var(--paper)).

## 2026-09-27 – v0.8.8
- Bright/dark button previews the mode it switches to: bright button with a
  dark sun in dark mode, dark button with a light moon in bright mode.

## 2026-09-27 – v0.8.7
- Bright/dark button: sun and moon are drawn as small inline SVG icons in the
  text colour (currentColor), like the scroll button arrows. The moon glyph
  from the font came out pale in bright mode.

## 2026-09-27 – v0.8.6
- User manual built into the app: "Help" in the menu (/help, template
  help.html). 11 sections: getting started, transcribing, reading, listening,
  teaching speakers, fixing words / spelling fixes, ads & clips, versions,
  settings, data / updates / uninstall, troubleshooting. Linked from the
  episode page hint and the READMEs.

## 2026-09-27 – v0.8.5
- Borders much stronger: --rule light #D9DDE4 -> #858E9C, dark #313A48 -> #66728A
  (was ~1.3:1, now at least 3:1 against panel and page background in both
  modes). Applies to panels, buttons, inputs, table lines, header/player edges.

## 2026-09-27 – v0.8.4
- Spelling fixes: the "All podcasts" switch saves in the background (fetch,
  JSON answer) without reloading the page. While saving, the knob turns into a
  spinner (shown at least 1/4 s); on failure the track flashes red, the state
  stays as it was and the tooltip says what went wrong. Without JS the form
  still submits normally.

## 2026-09-27 – v0.8.3
- Dark mode is the default; bright mode when the browser/system prefers light.
  Toggle button (sun / moon) floating bottom left, mirrored to the scroll
  buttons; the choice is remembered in the browser and applied in <head>
  before drawing (no flash). CSS: dark palette on :root:not([data-theme=light]),
  light palette repeated in @media (prefers-color-scheme: light) for
  :root:not([data-theme=dark]).
- Content and header keep clear of both floating button columns (narrower
  gutters on phones).
- Spelling fixes: "All podcasts" switch text lines up with the Remove button text.
- Installers: -y answers every question with yes, -n with no, --defaults /
  -Defaults takes the default answers without asking (Linux and Windows).
  Before, --yes / -Yes meant "defaults".

## 2026-09-27 – v0.8.2
- Spelling fixes list: "All podcasts" is an on/off switch showing the current
  state (tooltip says what a click does); the "For" column is gone; switch and
  Remove are right-aligned. Switch is a plain submit button (role="switch"),
  styled in CSS, works in light and dark mode.

## 2026-09-27 – v0.8.1
- install.sh: the background service (start at login) is now off by default
  ([y/N]); when updating an installation that already has it, the default is to
  keep it. Answering no removes an existing service. New --service option;
  --yes uses the defaults. (Windows already defaulted to no autostart.)
- Spelling fixes: switch an existing fix between "this podcast" and "all
  podcasts" from the list.

## 2026-09-27 – v0.8.0
- Spelling fixes (schema v8, table `spelling`): per podcast or for all podcasts,
  managed on the podcast page ("Shown as" + "Written wrong as", comma separated).
  Applied when showing / downloading a transcript (web, text download, CLI show);
  the raw transcript is untouched, so fixes apply to all episodes at once and
  removing one restores the original.
- Matching is strict: whole words, case-insensitive, punctuation around kept
  ("Film-Sack," / "film sack's" work), multi-word variants only as exactly those
  words in a row, longest variant first. Inside a text correction words are
  matched too, but never across its edge.
- Automatic rules: full names (2+ words) of all known people are shown as entered.
- Replaced words show the original on hover.
- Redirect messages now work with #anchors (query before the fragment).
- Tests: spelling_test.go.

## 2026-09-27 – v0.7.10
- Playback speed in 0.2 steps: 0.4× … 3×. A speed saved by an older version
  (0.5 steps) picks the nearest new step.

## 2026-09-27 – v0.7.9
- Job block in the full header (status, episode, progress, Start/Stop) is
  right-aligned like in the slim header, so its right edge stays put while
  scrolling (desktop widths; phones keep the stacked layout).

## 2026-09-27 – v0.7.8
- Header stays on top (sticky) and turns into a slim one-line bar once the page
  is scrolled more than 80 px (back to full size near the top): smaller logo and
  menu, job status on one line (state, stage, small progress bar, buttons);
  on phones the buttons are left out of the slim bar.
- The space the header gives up is kept as margin below it and scroll anchoring
  is off, so the page doesn't jump and can't flip between the two sizes.
- Scroll buttons count the header in their page size; the word menu opens below
  the word when it would otherwise end up under the header; anchor jumps keep
  clear of the header.

## 2026-09-27 – v0.7.7
- Header content (logo, menu, job status) uses the same 1500 px width and
  centering as the page content; the header bar itself stays full width.

## 2026-09-27 – v0.7.6
- Wider layout: page content up to 1500 px (was 980), transcript uses the full
  content width like the panels above it (was capped at 72 characters), player
  bar up to 1380 px. When the page fills the window, the content keeps clear
  of the floating scroll buttons.

## 2026-09-27 – v0.7.5
- Ad and movie-clip marks: select text (or click a speaker name / double-click a
  word) → "Mark as Ad / Movie clip / Normal". Stored as corrections of kind
  "mark" (Text = ad|clip|""), later marks win; independent of the speaker, so a
  host-read ad keeps its host.
- Marked paragraphs are tinted with a badge; paragraphs split where a mark starts
  or ends. Extra "Ads" / "Movie clips" lanes in the overview.
- "Hide ads" / "Hide movie clips" checkboxes (CSS-only hiding, choice remembered
  in the browser). "Download text without ads"; normal download tags [Ad] / [Movie clip].
- Movie clips never train voiceprints: no samples from passages ≥50 % inside a
  clip, nor from voices speaking ≥50 % in clips; marking a clip deletes this
  version's samples lying mostly inside it. Recognition skips turns ≥50 % in clips.
- Marks are carried over on "Transcribe again" / "Redo speaker detection".
- Tests: marks_test.go (split, clear, carry, clip share, sample deletion SQL).

## 2026-09-27 – v0.7.4
- Current word is underlined in the speaker's colour while playing (timed by
  whisper's word timestamps; runs every frame while playing, also after seeking).
- Floating page navigation on the right: top, one page up, jump to the part
  that is playing (shown once playback started), one page down, bottom.
  Page size leaves room for the player; hidden when the page doesn't scroll;
  smooth scrolling unless the system asks for reduced motion.

## 2026-09-27 – v0.7.3
- Word menu opens on DOUBLE-click (single click does nothing; the word
  selection the browser makes on double-click is dropped so the selection
  menu doesn't open on top). Dragging over words still opens the selection menu.
- "Play from here" starts at the full second before the word (43.77 s ->
  43.0 s) so the spot isn't missed; paragraph timestamps jump to exactly the
  second they show.

## 2026-09-27 – Player improvements (v0.7.2)
- Playback speed dropdown next to the player: 0.5x-3x in 0.5 steps,
  remembered per browser (localStorage, safe when blocked).
- Keys on the episode page: left/right = 1 s back/forward (Shift: 5 s),
  Space = play/pause; ignored while typing in a field or using a control.
- Clicking a single word opens the menu for that word: "Play from here"
  (top), who said it, correct the text. "Play from here" also in the menu
  for selections and paragraphs.
- Browser-tested: word menu, play from here lands on the word, keys, speed
  persisting after reload, no seeking while typing in the text box.

## 2026-09-26 – Speaker fixes after carry-over, loop retries (v0.7.1)
- Batz, 90 min episode with v0.7.0: whole song transcribed, one small
  repetition hiccup; speakers wrong in a bunch of places although the
  previous version was fully checked; sentence-final "." shown as its own
  speaker.
- "." bug: speakers were assigned per whisper TOKEN; punctuation tokens get
  unreliable timestamps (often inside the next speaker's turn) and, since
  carried corrections cover almost everything, were no longer absorbed.
  Now speakers are assigned per WORD (punctuation belongs to its word),
  timed only by the word's letter tokens. Test with the exact pattern:
  0.7.0 gave "Randy: . Well, okay", now "Scott: I don't care. | Randy: Well, okay".
- Carry-over now reproduces what the old version SHOWED: the displayed
  timeline of people / crosstalk / unknown (also passages the automatic
  recognition got right and the user left alone - before, those were
  re-recognized and could come out differently), each passage stretched up
  to 0.7 s into the gaps to its neighbours so shifted word timing still
  lands inside. Stored with origin "carried" (DB migration v7), shown as one
  line on the episode page with "Undo all"; text corrections stay individual.
- Loops: a piece is retried when a phrase of 3+ words repeats 4+ times (or
  2 words 6+ times); up to two rounds (sampling at 0.4, then 0.7); a retry
  is only kept if it repeats less - real repetition by the hosts survives.
  Cutting down to 2 + "[...]" still only at 6+ repetitions.
- Tests: period case, timeline carry-over (incl. accepted auto-recognition,
  stretching, unnamed voices not carried), repetition classification; end to
  end with migration 6 -> 7, retry and Undo all.

## 2026-09-26 – Transcription in pieces, text editing, carry-over (v0.7.0)
- Batz: turbo AND large-v3 drop chunks of speech (large-v3 fewer); no more
  loops with v0.6.2 settings. Almost every episode has a karaoke song by a
  host -> VAD would skip it.
- Transcription in pieces (default, Setup page): speaker detection now runs
  first; the WHOLE episode (songs/music included) is cut into pieces of max
  28 s at pauses / speaker changes, each transcribed on its own, all in one
  whisper-cli call (model loaded once, one -f/-of pair per piece). Whisper
  can't skip ahead any more. Progress shows "piece x of y".
- Loops per piece: a phrase of 3-12 words repeated >= 6 times back to back is
  a loop (hosts repeating something 2-5 times stays untouched); such pieces
  are transcribed again with sampling (-tp 0.4 -nf -bs 1); if still looping,
  cut down to 2 repetitions + "[...]" marker.
- Nothing is thrown away: a piece with detected speech but no text gets an
  "[unclear]" marker; words whisper wasn't sure about (token probability
  < 0.35) and text from pieces without detected voice are shown dotted grey.
  A piece whisper wrote no result for becomes [unclear] instead of failing.
- Text editing: select words (or click a speaker name) -> "Correct the text"
  in the same menu; stored as text corrections (DB migration v6), listed
  with Undo, used in transcript view and text download. Empty text deletes.
- Carry-over: "Transcribe again" and "Redo speaker detection" copy the
  previous version's manual corrections about people, crosstalk, unknown and
  text (voices named as a person become time ranges; voices of the new
  version that are >= 80 % inside one person's ranges become that person as
  a whole). If the re-downloaded audio differs in length (dynamic ads), the
  saved audio copy is used so the timeline still matches.
- Fixed: words in small gaps between turns took the raw voice label instead
  of the corrected person (gap filling now after corrections).
- Voice activity detection is now per podcast (Podcast settings, off by
  default, warning about songs); VAD model downloaded on demand.
- Tested end to end with a DB created by v0.6.2 (migration 5->6), a stand-in
  whisper that handles many files, loops in one piece, is unsure in another
  and returns nothing for a third; carry-over twice in a row; browser test of
  the text editing menu.

## 2026-09-25 – Repetition loops / skipped passages (v0.6.2)
- Batz, RTX 3070 fresh install: turbo transcripts miss parts (first minute,
  probably more); large-v3 instead produced a repetition loop at 07:17
  ("I don't care about that." x ~30, over a movie clip).
- Cause (known whisper behaviour on long audio): each 30 s window gets the
  previous window's text as prompt; on hard audio (music, clips) the model
  latches on to it -> loops, or skips ahead.
- New defaults: -mc 0 (no text carried between windows) and -et 2.8
  (repetitive output counts as a failed decode -> temperature fallback).
  Setup page "Transcription": context on/off (recommended off), optional
  voice activity detection (silero v6.2.0 model downloaded on demand;
  experimental - not verified here how it affects word timings).
  Self-test uses the same options. Log shows the options per episode.
- Loop finder: phrase of 3-12 words repeated back to back >= 5 times ->
  warning on the episode page with linked timestamps (test with the real
  text from Batz' episode).

## 2026-09-25 – Garbage transcript protection (v0.6.1)
- RTX 3070 / CUDA 13 build on CachyOS produced only "!" for a whole episode
  (185 segments for 1.5 h) while self-check + device detection said "CUDA,
  all fine". Known whisper.cpp GPU failure (numerical problem, typically with
  flash attention, which is on by default).
- Self-check now transcribes real speech (JFK sample from whisper.cpp,
  public domain, embedded) and checks the text; if it's garbage, it retries
  without flash attention (-nfa) and keeps that setting if it helps. Setup
  uses the same test for every GPU option (garbage -> next option), incl.
  the Linux install.sh builds.
- Every transcript is checked (mostly punctuation, < 30 words/min on long
  episodes, one sentence repeated in > 40 % of segments, no text at all);
  garbage -> automatic retry without flash attention -> otherwise the episode
  fails with a clear message instead of storing garbage
  (whisper-garbage-epN.log kept).
- "Find broken transcripts" button on the podcast page + CLI `verify`:
  checks finished episodes and queues broken ones again.
- Tests: quality check incl. the real "!!!" pattern; end-to-end with a
  stand-in whisper that fails with flash attention.

## 2026-09-25 – First real Windows/AMD run (v0.6.0)
- Batz: Windows PC, AMD Radeon RX 7900 XTX, install.cmd -> Vulkan verified
  (Task Manager: Compute ~62 %, 3.9 GB VRAM during transcription).
- ~1.5 h Film Sack episode: whisper turbo 8m53s on Vulkan (~10x real time),
  speaker detection 10m34s on CPU (ResNet34, 1 s, 0.5 -> 34 voices, same as
  on the other PC), total ~20 min per episode (~4.4x real time).
- RTX 3070 / CachyOS / CUDA, ~1.5 h episode (Film Sack 761): whisper 14m36s,
  speaker detection 10m48s (threshold 0.52, 29 voices), identify 54s:
  83 % of speech recognized (4 people named on that machine).
  -> 7900 XTX on Vulkan is faster than 3070 on CUDA: ROCm not needed.
  Open question: only 185 whisper segments for ~1.5 h (AMD run: 2356) -
  check transcript for gaps / repetitions.

## 2026-09-25 – Windows installer + Vulkan (v0.6.0)
- Windows Vulkan build of whisper.cpp v1.9.2 (whisper.cpp publishes none):
  cross-compiled with MinGW-w64, static, only imports vulkan-1.dll (from the
  graphics driver) + system DLLs; shipped in the zip as gpu\vulkan\whisper-cli.exe.
  Recipe in third_party/whisper-vulkan-win64/BUILD.md (import lib from
  vulkan-1.def, SPIR-V headers, MinGW compat header for
  THREAD_POWER_THROTTLING_STATE, generated shader data at -O0, 120 MB
  mul_mm shader file split into 4 parts to compile in < 4 GB RAM).
  Checked under Wine: starts, Vulkan backend initializes, falls back to CPU
  without a real device.
- Setup: graphics card choice Automatic / NVIDIA (CUDA) / Vulkan / Processor
  (Setup page + `setup --gpu`). Automatic: CUDA for NVIDIA with a new enough
  driver, Vulkan for NVIDIA/AMD Radeon/Intel Arc (not AMD APU or Intel UHD
  graphics) if vulkan-1.dll exists, else processor. Every GPU option is
  verified with a short test transcription; if whisper doesn't report the
  GPU, setup falls back CUDA -> Vulkan -> processor. Setup order changed:
  ffmpeg + models first, then whisper (needed for the test).
- Graphics adapters listed via WMI on Windows (Setup page shows them).
- install.cmd / install.ps1: checks (64-bit Windows 10+, disk space), graphics
  hints, stops a running installed copy, copies to %LOCALAPPDATA%\podscribe
  (keeps data), removes the downloaded-file mark (Unblock-File), runs setup,
  Start menu shortcut, optional desktop shortcut and autostart (minimized,
  serve --no-browser), uninstall.cmd (runs from %TEMP%, can keep data),
  install.log. Parse-checked with PowerShell 7.6; not run on real Windows yet.
- Unnamed voices got distinguishable muted colours again (0.5.0 made them all
  grey, which hid manual speaker assignments).
- Tests: GPU name classification, marker/binary match.

## 2026-09-25 – v0.5.4
- Batz' RTX 3070: whisper ran on the CPU although "cuda" was reported. Its
  whisper.log showed the generic CPU build ("loaded CPU backend from
  libggml-cpu-haswell.so", "no GPU found"): copying the old PC's data folder
  had replaced the CUDA whisper-cli, but the BUILD_INFO marker stayed, so
  podscribe and install.sh kept trusting it.
- BUILD_INFO now stores the sha256 of the binary it describes; podscribe
  ignores the marker if whisper-cli changed (unit test).
- install.sh only keeps an existing GPU build if the hash matches and the
  binary really links CUDA (libcudart) / Vulkan (libvulkan); otherwise it
  removes it and builds again (tested with a replaced binary).

## 2026-09-25 – v0.5.3
- Batz ran the unpacked download folder (~/Downloads/podscribe-0.5.1) instead
  of the installed copy (~/.local/share/podscribe with the CUDA build) - each
  copy has its own data folder, so the download copy used a CPU whisper.
  podscribe now prints a NOTE at startup when an installed copy exists and a
  different one is running.
- Fixed self-check: CPU builds print "device 0: CPU (type: 0)", which was
  reported as "CUDA". Only lines naming a CUDA/Vulkan device count now
  (test with real whisper output lines).

## 2026-09-25 – GPU reporting fixes (v0.5.2)
- Batz: installer built CUDA version on CachyOS + RTX 3070 (self-check found
  the card), but podscribe then claimed "no NVIDIA graphics card found".
  Causes: on Linux the Setup page GPU line was never filled (detection only
  ran on Windows), and the backend shown came from a stored setting that was
  stale after copying the database from the CPU-only PC.
- Each episode now records the device whisper ACTUALLY used (parsed from
  whisper's output: CUDA / Vulkan / processor) - shown in the log
  ("transcribed ... on the graphics card (CUDA)") and in the versions list.
  Warning + whisper output in the log if a GPU build is installed but
  whisper ran on the processor.
- Installed backend: a local GPU build (BUILD_INFO) now always wins over the
  stored setting; Linux setup also detects the NVIDIA card for display.

## 2026-09-25 – Linux installer (v0.5.1)
- install.sh (shipped in the Linux package): checks system (x86_64, glibc
  >= 2.34), detects package manager (apt/dnf/pacman/zypper), installs ffmpeg
  (Fedora: ffmpeg-free), copies podscribe to ~/.local/share/podscribe.
- Graphics: detects NVIDIA/AMD/Intel via /sys; NVIDIA driver via nvidia-smi
  (tells how to install the driver per distro if missing - never installs
  drivers itself). Tries CUDA build of whisper.cpp v1.9.2 (installs CUDA
  toolkit where the distro has one, needs >= 12.0 and a driver that supports
  it, builds only for the own card's architecture), then Vulkan build
  (NVIDIA/AMD/Intel, needs a real Vulkan device), else CPU version.
  Free-RAM check before building (CUDA 4 GB, Vulkan 6 GB) and parallel jobs
  limited by RAM - one generated Vulkan source file is 120 MB and needs ~4 GB
  alone (the 3 GB sandbox build was killed by the OOM killer).
- Built whisper-cli is marked with data/tools/whisper/BUILD_INFO; app setup
  keeps it instead of downloading the CPU build; re-running the installer
  keeps an existing build of the same version.
- Runs setup (model: turbo with GPU, turbo-q5 on CPU) and requires the
  self-check to pass; self-check now also recognizes Vulkan devices; Setup
  page shows CUDA/Vulkan/processor version.
- Launchers: ~/.local/bin/podscribe, app menu entry, optional systemd user
  service (starts at login, Nice=10), uninstall.sh, install log.
- Tested in sandbox: full run with a stand-in GPU build (checks, packages,
  copy, keep-build, setup, self-check with Vulkan, launchers), re-run/update,
  uninstall; shellcheck clean. NOT tested: real CUDA/Vulkan compile + GPU run
  (no GPU, too little RAM), systemd service (no user session in sandbox).

## 2026-09-25 – Linux package (v0.5.0)
- podscribe-0.5.0-linux-x64.tar.gz: binary + libsherpa-onnx-c-api.so +
  libonnxruntime.so + README; binary built with rpath $ORIGIN so the folder
  is portable (tested with the build machine's module cache hidden: libraries
  load from the folder, web UI starts, diarization models load).
- Requires glibc >= 2.34 (Ubuntu 22.04+, Debian 12+, Fedora 35+).
- README-linux.txt: ffmpeg requirement, headless use via SSH tunnel, building
  whisper.cpp with CUDA for NVIDIA GPUs.
- build.sh now produces the Linux tarball too.

## 2026-09-24 – Phase 3: people + speaker recognition (v0.5.0)
- People are global (DB migration v5: people, feed_people roster,
  voice_samples). Old per-feed speaker names become people + hosts.
- People page (samples count/length, podcasts), person page (voice samples
  with links to the passage, remove sample, rename, remove person).
- Podcast page: "People on this podcast" roster - Host / Regular / –, add
  new hosts/regulars by name. Names given when adding a podcast become hosts.
- Correction menu offers people (podcast's hosts and regulars first), "New
  person…" with name field, unnamed voices, crosstalk, unknown. Assigning a
  passage (>= 2 s) or a whole voice to a person saves a confirmed voice
  sample (ResNet34 voiceprint, up to 30 s); undoing the correction removes it.
- Speaker recognition per turn: each turn (>= 1.5 s, not crosstalk) is
  compared with each person's voiceprint (average of confirmed samples);
  needs similarity >= 0.55 (people not on the podcast's roster +0.05) and a
  margin of 0.03 over the second best - otherwise it stays unnamed. Short
  turns follow their voice's clear (>= 70 %) majority. Voices the user named
  manually are left alone. Stored as automatic corrections (manual ones win).
- Runs automatically after transcription / "Redo speaker detection" once
  people with samples exist; "Identify speakers" button for existing
  versions (background job, progress shown).
- Unnamed voices are shown in neutral grey (dotted), people in colour.
- Measured end to end on synthetic 4-voice episodes (name voices in episode
  A via the UI, recognize episode B): 2 samples/person (~9 s): 84 % correct,
  14 % wrong, 2 % unnamed; 5 samples (~23 s): 79 % correct, 5 % wrong, 16 %
  unnamed (ambiguous turns between two very similar voices are left unnamed
  on purpose). Tried comparing relative to the common voice component: more
  named (83 %) but twice the errors (12 %) -> not used; wrong names are worse
  than unnamed ones.
- Fixed: redirect after some actions produced "?v=18?msg=..." (broken link).
- Fixed: "New speaker" numbering ignored person labels.
- Correction menu opens upwards when there is no room above the player.

## 2026-09-24 – Voice merging experiment + measurements (v0.4.1)
- Batz real episode: threshold 0.5 keeps different people apart better, 0.55
  joins more fragments of one person -> no single threshold fits.
- Built second-pass voice merging (compare per-voice ResNet34 voiceprints,
  merge most similar pairs above a threshold; stored as automatic, undoable
  corrections; "Merge similar voices" button re-runs it in ~1 s; similarity
  of top pairs is written to the log). Voices without clean speech now also
  get a voiceprint.
- Built an evaluation setup: synthetic English 4-speaker "podcast" (sherpa
  VITS libritts_r TTS, 324 s, 88 turns incl. short backchannels) with known
  truth. Dev tests (env-gated): tts_gen_test.go, eval_test.go,
  pure_test.go, turnmatch_test.go.
- Findings:
  - Clean ~30 s voiceprints (ResNet34): same person 0.94-0.98, different
    people <= 0.91 -> the model itself separates well.
  - Detected voices are impure (47-53 s of 324 s in the wrong voice), so their
    voiceprints sit in between -> merging needs ~0.92 to avoid wrong merges
    and then only merges little (10->8, 8->7). => shipped OFF by default.
  - CAM++ performed clearly worse than ResNet34 here (25 voices, weak
    separation on single utterances) -> keep ResNet34 as default.
  - Share of time attributed correctly (best-case naming of detected voices):
    1 s/0.5: 91.4%, 2.5 s/0.5: 69.3% (!), 2.5 s/0.3: 88.0%.
  - Matching every turn against KNOWN clean voiceprints instead of trusting
    sherpa's voice groups: 91.4% / 84.9% / 93.4% -> more robust, especially
    with fast settings. This shapes phase 3.
  - Caveat: synthetic voices, no real crosstalk/music - real audio will be worse.

## 2026-09-24 – Manual speaker corrections (v0.4.0)
- Three ways to correct speakers on the episode page:
  - select words (also across paragraphs) -> assign to a speaker
  - click the speaker name of a paragraph -> reassign the whole paragraph
  - click a speaker name in the lanes -> "everything said by X is actually Y" (merge)
  Targets: any existing speaker, "New speaker", "Several people at once",
  "Unknown / not speech".
- Corrections are stored as edits on top of the automatic result (DB
  migration v3: corrections table), listed per version with "Undo".
  Merges apply first (chains resolved, cycles ignored), then ranges (later
  wins); merges also apply to speakers created by corrections.
- Fuzzy-timing cleanup never touches manually assigned words or their
  neighbours; manual ranges are never collapsed into the segment majority.
- Transcript is rendered word by word with timings (needed to map a text
  selection to exact times); speaker lanes are now built from the corrected
  result so they always match the text; shares shown with rounding.
- Fixed: characters like é/ü split across two whisper tokens came out as "�"
  (token text now decoded byte-preserving; applies to new transcriptions).
- Fixed: closing the app during "Redo speaker detection" put the episode back
  to "Waiting" on restart.
- Tests for corrections, word building, UTF-8 split fix.

## 2026-09-24 – Redo speaker detection (v0.3.1)
- First real test (Batz, 1660 Super, app v0.2.0 defaults: ResNet34, 1 s step,
  threshold 0.3): whisper 23 min, speaker detection 36 min, 123 voices for ~11
  real ones (4 hosts, 4-6 movie clip voices, 1 voice in intro music)
  -> threshold 0.3 far too low for real English podcast audio.
- "Redo speaker detection" (episode page button + CLI `rediarize`): reuses the
  transcript and the saved audio copy of a version, runs only speaker
  detection with the current settings, stores a new version. Makes tuning
  cost minutes instead of a full re-transcription.
- Default separation threshold raised 0.3 -> 0.5.
- Episode header shows the speaker settings of the version being viewed.

## 2026-09-24 – Speaker detection settings (v0.3.0)
- Found cause of slow speaker detection (~30 min on first real test): sherpa's
  default 1 s window step + heavy ResNet34 model, all on CPU.
- Benchmarked on test audio: 2.5 s step ~2.4x faster, 5 s step ~4x;
  CAM++ and TitaNet small ~2x faster than ResNet34. Cluster count depends on
  model + step + threshold together.
- Settings (Setup page "Speaker detection", CLI `config`): voice model
  (resnet34 / campplus / titanet), precision (1 / 2.5 / 5 s step), separation
  threshold. Defaults unchanged (resnet34, 1 s, 0.3).
- Selected model is downloaded automatically before the next episode.
- Voiceprints are ALWAYS computed with ResNet34, independent of the setting,
  so they stay comparable across episodes for speaker learning.
- Each version records the speaker settings used (DB migration v2:
  versions.diarize_info) - shown in the versions list (UI + CLI).

## 2026-09-24 – Phase 2: local web UI (v0.2.0)
- Double-click / no arguments now starts the web UI (`serve`); browser opens
  automatically; finds a free port from 8321; bound to 127.0.0.1 only.
- Background worker (one job at a time: queue or setup) with graceful stop
  ("after this episode") and immediate stop; shared queue loop with the CLI.
- Live status via Server-Sent Events: stage, percent (download, transcription),
  current episode, last error; live activity log page.
- Pages: Podcasts overview (progress per feed, recent activity), Add podcast,
  Podcast page (settings, check for new episodes, filter tabs All/Done/Waiting/
  Failed, per-episode "Transcribe now/again"), Episode page, Setup, Activity.
- Episode page: speaker lane timeline (who speaks when, share %, click to seek),
  transcript with speaker colours, click timestamp to play, current line
  highlight while playing, sticky audio player (Range requests -> seeking),
  versions list with "make main version", download as .txt.
- Setup page: installed-component checklist, GPU info, model choice, CPU-only
  and re-download options; runs setup in the background with progress.
- Episodes are claimed atomically (UI worker + CLI `run` can't double-process).
- POSTs from other websites are rejected (Origin/Referer check).
- Forms work without JS; JS (vanilla, embedded) only adds live updates + audio.
- System fonts only (offline); light/dark mode; responsive; checked with
  screenshots (desktop, dark, mobile, running state).
- Errors when started by double-click keep the window open.

## 2026-09-24 – Phase 1: core pipeline (v0.1.0)
- Researched/pinned components: whisper.cpp v1.9.2 (Windows CUDA 12.4 build incl.
  CUDA runtime DLLs), sherpa-onnx-go v1.13.8 (prebuilt Windows DLLs), pyannote
  segmentation 3.0 + wespeaker resnet34 (English) embedding models, BtbN ffmpeg.
- Project skeleton, portable `data/` layout, log file `data/podscribe.log`.
- SQLite schema: settings, feeds, episodes, versions, segments, tokens, turns,
  cluster_embeddings (raw results, see SPEC.md).
- `setup`: NVIDIA detection via nvidia-smi (driver version check), downloads
  whisper.cpp (CUDA or CPU), ffmpeg (Windows), whisper model (selectable),
  diarization models; progress display; ends with self-check.
- `check`: ffmpeg version, test transcription reporting CUDA device vs CPU,
  diarization model load.
- `feed add/set/list/refresh`: RSS parsing (itunes:duration, many date formats,
  Latin-1 feeds, items without audio skipped, guid fallback to audio URL).
- `run`: queue oldest-first, auto feed refresh, --limit, --retry-errors,
  stops after 3 failures in a row, requeues episodes stuck in 'processing'.
- `process <id>`: re-process any episode → new version, old kept.
- Pipeline: download → ffmpeg 16 kHz mono wav → whisper-cli -ojf (progress
  shown) → diarization → per-cluster embeddings → compact Opus copy.
- Merge: tokens labeled by speaker time overlap, segments split at speaker
  changes, tiny runs absorbed, crosstalk + Unknown labels, same-speaker lines merged.
- `show` (console or --out file), `versions`, `episodes`, `config`.
- Ctrl+C: once = finish episode then stop, twice = abort + requeue.
- Cluster ids renumbered contiguously (sherpa returns gaps).
- Diarization threshold tested (0.3/0.5/0.7) → default 0.3, configurable.
- whisper-cli runs with relative paths from work dir (non-ASCII path safety).
- Double-clicking the exe on Windows keeps the window open with a hint.
- Unit tests: overlap regions, utterance building/crosstalk, feed parsing.
- Tested in Linux sandbox end-to-end with a local feed and a stub whisper
  (real whisper model not downloadable there); diarization tested with real audio.
- build.sh: Linux build + Windows cross-compile (MinGW) + zip with DLLs.
