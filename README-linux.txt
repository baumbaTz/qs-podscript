QS-PodScript - podcast transcription with speaker detection (Linux x64)
====================================================================

Needs: glibc 2.34 or newer (Ubuntu 22.04+, Debian 12+, Fedora 35+, ...)
       and ffmpeg (e.g. sudo apt install ffmpeg)

Keep all files together in one folder. Everything the program downloads and
creates goes into a "data" folder next to it. Delete the folder to uninstall.

FORMERLY "podscribe": install.sh moves an existing podscribe installation
(~/.local/share/podscribe) over to QS-PodScript and keeps all its data.

MANUAL: once QS-PodScript runs, click "Help" at the top of the page.

EASIEST: run the installer from this folder
  ./install.sh
  It installs ffmpeg, detects your graphics card, builds a GPU version of the
  transcription engine if possible (NVIDIA: CUDA, other cards: Vulkan),
  downloads the models, runs a self-check and adds a menu entry. It asks
  whether QS-PodScript should run in the background and start when you log in
  (default: no). Options: ./install.sh --help

SPEAKER DETECTION ON THE GRAPHICS CARD (optional, NVIDIA only)
  Speaker detection (telling voices apart) normally runs on the processor and
  takes as long as the transcription or longer; on the graphics card it was
  about ten times faster in a first test (RTX 3070: ~4 minutes for an episode
  of over an hour). Switch it on in QS-PodScript: Setup -> Speaker detection
  -> "Use the graphics card for speaker detection". install.sh asks too when
  it finds an NVIDIA card (--gpu-speakers: without asking), and so does
    qs-podscript gpu-speakers install       (status / remove)
  It downloads sherpa-onnx's graphics-card build (the speaker detection
  library with CUDA support, plus its ONNX Runtime) and the NVIDIA libraries
  it needs (CUDA 13, cuDNN 9 - about 1.3 GB download, 2 GB on disk, kept
  inside the QS-PodScript folder; libraries already installed on the system
  are used instead), then tests it. Needs a GeForce RTX 20 / GTX 16 series
  card or newer and NVIDIA driver 580 or newer
  (nvidia-smi shows "CUDA Version: 13.x"). QS-PodScript tests it at every
  start and uses the processor whenever it doesn't work.
  Check: qs-podscript gpu-check (prints the times on CPU and graphics card).
  Remove it: Setup, qs-podscript gpu-speakers remove, or ./install.sh --no-gpu-speakers
  Older NVIDIA cards (GTX 10 series and before; CUDA 13 dropped them), AMD
  and Intel cards: no ready-made speaker detection for them - it stays on
  the processor (transcription uses them through Vulkan).
  Compare speed and voice models on one episode (nothing is saved):
    qs-podscript speaker-bench episode.mp3 [resnet34,resnet293,titanet-large] [--cpu]

MANUAL START (without installer)
  ./qs-podscript
  Opens the web interface in your browser (http://127.0.0.1:8321/).
  Keep the terminal open; Ctrl+C quits.
  Without a desktop/browser on this machine: ./qs-podscript serve --no-browser
  (it only listens on 127.0.0.1 - use an SSH tunnel to reach it remotely:
   ssh -L 8321:127.0.0.1:8321 user@host)

FIRST TIME
  Setup page -> Install. On Linux this installs the CPU version of whisper.cpp.

NVIDIA GPU BY HAND (only if you don't use install.sh)
  whisper.cpp has no reliable prebuilt CUDA version for Linux, so build it
  once yourself (needs the CUDA toolkit, e.g. nvidia-cuda-toolkit):

    git clone https://github.com/ggml-org/whisper.cpp && cd whisper.cpp
    git checkout v1.9.2
    cmake -B build -DGGML_CUDA=1 -DBUILD_SHARED_LIBS=OFF -DCMAKE_BUILD_TYPE=Release
    cmake --build build -j --target whisper-cli
    cp build/bin/whisper-cli /path/to/qs-podscript/data/tools/whisper/

  Do this after the first setup (it replaces the CPU version), and create
  data/tools/whisper/BUILD_INFO containing the line  backend=cuda
  so setup keeps your build. The self-check
  in the Activity log then shows your graphics card as CUDA device.

COMMAND LINE
  ./qs-podscript help

PROBLEMS
  Send the file data/qs-podscript.log

HOMESERVER (shared, multi-user) - e.g. in an LXC container
  The same program runs as a server: everybody can read the transcripts,
  logged-in editors fix the podcasts an admin assigned to them.
  The server doesn't transcribe itself (unless switched on: server Setup ->
  "Who transcribes"). The work is done by helpers' computers: a QS-PodScript
  on a PC with a graphics card that added the server (Setup -> "Where you
  work" -> "Add a server", or when installing: ./install.sh asks once, or
  ./install.sh --connect <address> --connect-user NAME --connect-work, or
  later: qs-podscript connect <address> --user NAME --work) with "Transcribe
  for this server" ticked. Each
  one takes one episode at a time; several work in parallel. Keep the server
  and all helpers on the same QS-PodScript version.
  Easiest: install it as a server in one go (system service
  "qs-podscript-server", asks for the first admin login and its password):
    ./install.sh --server                                (proxy on this machine)
    ./install.sh --server --trusted-proxy <proxy-ip>     (proxy elsewhere;
                                                          listens on 0.0.0.0:8322)
  More options: ./install.sh --help (--listen, --admin <name>, --no-service).
  Updating: run the newer package's ./install.sh again - it finds the service
  and keeps its settings (listen address, proxy).
  By hand instead:
    ./qs-podscript user add <yourname> --admin      (asks for a password)
    ./qs-podscript server --listen 127.0.0.1:8322
  Put a reverse proxy with HTTPS in front (nginx / Caddy / Traefik) and pass
  the Host header through, e.g. nginx:
    location / { proxy_pass http://127.0.0.1:8322; proxy_set_header Host $host;
                 proxy_set_header X-Forwarded-Proto $scheme;
                 proxy_set_header X-Forwarded-For $remote_addr;
                 client_max_body_size 0; }
  Traefik (file provider; Host and X-Forwarded-* are passed by default):
    http:
      routers:
        qs-podscript:
          rule: Host(`podscript.example.org`)
          entryPoints: [websecure]
          service: qs-podscript
          tls: { certResolver: letsencrypt }
      services:
        qs-podscript:
          loadBalancer:
            servers: [{ url: "http://<server-ip>:8322" }]
    Helpers upload episode audio with their results. Traefik v3 cuts request
    bodies after 60 s by default; if uploads from slow lines fail, raise it in
    the static config:  entryPoints.websecure.transport.respondingTimeouts.readTimeout: 600s
    Strict Content-Security-Policy (optional): the pages load everything
    from the server itself and have no inline scripts or styles, so this
    works (e.g. in your secure-headers middleware):
      middlewares:
        secure-headers:
          headers:
            contentSecurityPolicy: "default-src 'self'; img-src 'self' data:; media-src 'self'; style-src 'self'; script-src 'self'; connect-src 'self'; frame-ancestors 'none'; base-uri 'self'; form-action 'self'"
  If the proxy runs in another container/machine, listen on the network and
  tell the server which address the proxy has (otherwise all visitors look
  like the proxy and share one login-attempt limit):
    ./qs-podscript server --listen 0.0.0.0:8322 --trusted-proxy <proxy-ip>
  Only the proxy should be able to reach port 8322 (firewall).
  The service install.sh --server writes (/etc/systemd/system/qs-podscript-server.service):
    [Unit]
    Description=QS-PodScript server
    After=network-online.target
    [Service]
    ExecStart=/root/.local/share/qs-podscript/qs-podscript server --listen 0.0.0.0:8322 --trusted-proxy <proxy-ip>
    Restart=on-failure
    [Install]
    WantedBy=multi-user.target
  Users are managed on the "Users" page (admins) or with: qs-podscript user list|add|passwd|admin|delete
  To start with existing work: export it on your PC (Setup page -> Export),
  copy the file to the server, then
    systemctl stop qs-podscript-server
    ./qs-podscript import export.zip
    ./qs-podscript user add <yourname> --admin
    systemctl start qs-podscript-server
  The import replaces the server's database including its logins (the old
  data is kept in data/before-import-...), so add the users again and
  reconnect the helpers' computers.

