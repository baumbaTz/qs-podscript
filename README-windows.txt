QS-PodScript - podcast transcription with speaker detection (Windows)
===================================================================

Formerly "podscribe": install.cmd moves an existing podscribe installation
(%LOCALAPPDATA%\podscribe) over to QS-PodScript and keeps all its data.

INSTALL (recommended)
  1. Unzip the whole package.
  2. Double-click install.cmd
     (If Windows asks "Do you want to run this file?" / SmartScreen:
      "More info" -> "Run anyway". The package is not code-signed.)

  The installer
   - copies QS-PodScript to %LOCALAPPDATA%\qs-podscript (no admin rights needed)
   - checks your graphics card and picks the fastest option that really
     works - each one is tested with a short transcription:
       NVIDIA  -> CUDA (fastest), otherwise Vulkan
       AMD Radeon / Intel Arc -> Vulkan
       nothing suitable -> processor (slower, but works)
   - downloads ffmpeg, whisper.cpp and the models (about 2.5 GB, once)
   - with an NVIDIA card of the RTX 20 / GTX 16 series or newer (driver 580
     or newer): offers speaker detection on
     the graphics card too - about ten times faster than on the processor;
     about 1.5 GB more download (NVIDIA's CUDA 13 and cuDNN 9 libraries, kept
     inside the QS-PodScript folder). Also later: Setup -> Speaker detection.
     Older NVIDIA cards (GTX 10 and before), AMD and Intel cards can't do
     this part (transcription still uses them).
   - creates a Start menu entry, optionally a desktop shortcut and autostart
   - writes uninstall.cmd into the install folder

  Running install.cmd from a newer package later updates QS-PodScript and keeps
  all transcripts, people and settings.

  Options, e.g. from a terminal in the unzipped folder:
    install.cmd -Gpu vulkan        force Vulkan (auto, cuda, vulkan, cpu)
    install.cmd -Model turbo-q5    other speech model
    install.cmd -y                 answer every question with yes
    install.cmd -n                 answer every question with no
    install.cmd -Defaults          no questions, default answers
    install.cmd -GpuSpeakers       speaker detection on the NVIDIA card, no question
    install.cmd -NoGpuSpeakers     speaker detection on the processor (removes that part)

USE
  Start "QS-PodScript" from the Start menu. A small window opens (keep it open)
  and your browser shows QS-PodScript at http://127.0.0.1:8321/
  Closing that window quits QS-PodScript.

  The full manual is built in: click "Help" at the top of QS-PodScript.

  Setup page: speech model, graphics card choice (Automatic / NVIDIA CUDA /
  Vulkan / Processor), transcription and speaker detection settings.
  "Update installation" re-runs the graphics card test.

  Correcting: on an episode page select words or click a speaker name to
  assign the speaker or correct the text. "Transcribe again" keeps your
  corrections. Dotted grey words: Whisper wasn't sure. [unclear]: speech
  that Whisper couldn't transcribe.
  "Look Who's Talking Now" (podcast page) lets you check speakers in pieces of about
  half a minute instead of whole episodes.

SHARED SERVER
  Setup -> "Where you work" -> "Add a server" (several are possible). Then
  switch to a server with the menu next to the name at the top: Podcasts,
  Search and People show that server until you switch back. Tick
  "Transcribe for this server" to let this computer transcribe episodes for
  it after your own queue.
  The installer can do this too: it asks once whether to connect to a shared
  server (address, your name, password - not shown), or pass it directly:
    install.cmd -Connect https://transcribe.example.org -ConnectUser NAME -ConnectWork
  Or from a command prompt in the QS-PodScript folder:
    qs-podscript.exe connect https://transcribe.example.org --user NAME --work

WITHOUT INSTALLER
  qs-podscript.exe also runs straight from the unzipped folder; everything it
  downloads then goes into a "data" folder next to it. Don't mix both ways -
  each copy has its own data.

GRAPHICS CARD NOTES
  - Keep the graphics driver up to date. NVIDIA needs driver 551.61 or newer
    for CUDA; Vulkan comes with every current AMD, NVIDIA and Intel driver.
  - Speaker detection runs on the processor on Windows (on Linux it can use
    an NVIDIA card); the transcription uses the graphics card.

MOVING TO ANOTHER COMPUTER
  Setup page -> "Moving to another computer": Export makes one file with
  everything you did (podcasts, transcripts, corrections, people, voice
  samples, audio copies). On the other computer: Setup -> Import, then
  restart QS-PodScript. Tools and models are downloaded there by the setup.

PROBLEMS
  Send data\qs-podscript.log (and install.log from the install folder).
