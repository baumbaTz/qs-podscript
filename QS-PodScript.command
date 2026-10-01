#!/bin/sh
# Double-click in the Finder: starts QS-PodScript and opens it in the browser.
# Keep the Terminal window open while you use it; close it to quit.
cd "$(dirname "$0")" || exit 1
# Homebrew's programs (ffmpeg, whisper-cli) are not on the Finder's PATH
PATH="/opt/homebrew/bin:/usr/local/bin:$PATH"
export PATH
exec ./qs-podscript
