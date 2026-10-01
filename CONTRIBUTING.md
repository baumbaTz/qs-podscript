# Contributing

Thanks for helping! A few notes so things stay simple.

## Reporting problems

- Open an [issue](https://github.com/baumbatz/qs-podscript/issues/new/choose).
- Say which **version** (bottom of Help → "What QS-PodScript uses"), which
  **system** and which **graphics card**.
- Attach the **log file**: `data/qs-podscript.log` next to the program
  (Activity page → path at the top). It contains no passwords; it may contain
  podcast and episode titles.

## Code

- Go 1.24+, standard library first. The web pages are server-rendered
  (`html/template`), styled with one CSS file and work **without
  JavaScript**; JavaScript only adds conveniences (live status, drag and
  drop, audio). Please keep it that way – no frameworks, no CDNs, everything
  embedded so it runs offline.
- `gofmt`, `go vet -tags sqlite_fts5 .` and `go test -tags sqlite_fts5 ./...`
  must pass (CI checks them).
- Database changes go through the numbered migrations in `store.go`
  (`schemaVersion`), never by editing old steps.
- Keep the manual (`web/templates/help.html`) in step with UI changes, and
  add a line to `DONE.md` (change log) for anything user-visible.
- Texts in the UI are plain English, written for non-technical people.

## Licence

By contributing you agree that your contribution is licensed under the
project's licence, the GNU AGPL-3.0-or-later.
