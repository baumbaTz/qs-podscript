QS-PodScript - podcast transcription with speaker detection (macOS, Apple Silicon)
=================================================================================

Status: NEW and not tested on a real Mac yet - feedback very welcome:
https://github.com/baumbatz/qs-podscript/issues

Needs: macOS 13 or newer on an Apple Silicon Mac (M1/M2/M3/M4) and Homebrew
(https://brew.sh).

1. INSTALL THE TOOLS (once), in the Terminal:
     brew install ffmpeg whisper-cpp
   whisper.cpp from Homebrew uses the Mac's GPU (Metal) by itself.

2. PUT THE FOLDER WHERE YOU LIKE, e.g. in your home folder or in
   ~/Applications. Keep all files together: everything QS-PodScript downloads
   and creates goes into a "data" folder next to it. Delete the folder to
   uninstall. (Not into /Applications - the program must be able to write
   next to itself.)

3. ALLOW IT TO RUN (once). The app is not signed by Apple, so macOS blocks
   it at first. In the Terminal, inside the folder:
     xattr -dr com.apple.quarantine .
   (or: right-click QS-PodScript.command -> Open -> Open, and confirm the
   same for qs-podscript in System Settings -> Privacy & Security.)

4. START: double-click QS-PodScript.command. A Terminal window opens and the
   web interface opens in your browser (http://127.0.0.1:8321/). Keep the
   Terminal window open while you use it; close it to quit.
   Or in the Terminal: ./qs-podscript

5. FIRST START: Setup downloads the speech and speaker models (about 2.5 GB)
   and runs a self-check.

MANUAL: once QS-PodScript runs, click "Help" at the top of the page.

SHARED SERVER: Setup -> "Where you work" -> "Add a server". Then switch to a
server with the menu next to the name at the top. Tick "Transcribe for this
server" to let this Mac transcribe episodes for it.

Speaker detection runs on the processor on a Mac (fast enough on Apple
Silicon); transcription uses the GPU through Metal.
