#!/usr/bin/env bash
# QS-PodScript installer for Linux (x86_64)
#
# Installs everything QS-PodScript needs, detects the graphics card and builds a
# GPU version of the transcription engine if possible:
#   NVIDIA + CUDA toolkit  -> CUDA build   (fastest)
#   any GPU with Vulkan    -> Vulkan build (NVIDIA, AMD, Intel)
#   otherwise              -> CPU version  (works everywhere, slow)
# Then it downloads the models, runs a self-check and, if you want (default:
# no), sets QS-PodScript up as a background service that starts when you log in.
#
# Usage:  ./install.sh [options]
#   --dir DIR      install location (default: ~/.local/share/qs-podscript)
#   --cpu          don't try to use the graphics card
#   --model NAME   speech model (default: turbo with GPU, turbo-q5 without)
#   --gpu-speakers     speaker detection on the NVIDIA graphics card too without
#                      asking (about 1.3 GB of NVIDIA libraries; also in Setup)
#   --no-gpu-speakers  speaker detection on the processor (removes that part)
#   --service      run QS-PodScript in the background, starting at login
#   --no-service   don't offer the background service
#   -y, --yes      answer every question with yes
#   -n, --no       answer every question with no
#   --defaults     don't ask, use the default answers
#   --help
#
# Server (shared homeserver, e.g. an LXC container):
#   --server               install as server: a system service
#                          "qs-podscript-server" instead of the menu entry
#   --listen ADDR          address to listen on (default 127.0.0.1:8322, or
#                          0.0.0.0:8322 when --trusted-proxy is given)
#   --trusted-proxy IP     address of the reverse proxy if it runs on another
#                          machine/container (comma-separated for several)
#   --admin NAME           create the first admin login (asks for a password)
#
# Connecting this computer to a shared server (optional, like Setup -> "Add
# a server"; without these options the installer asks once):
#   --connect ADDRESS      the server, e.g. https://transcribe.example.org
#   --connect-user NAME    your login there (the password is asked for, not
#                          shown - or set QSPODSCRIPT_PASSWORD)
#   --connect-label TEXT   name for the server on this computer
#   --connect-work         "Transcribe for this server" (helps when you press
#                          "Start transcribing")
# Example:  ./install.sh --server --trusted-proxy 192.168.1.10 --admin batz
# Updating a server: just run the new install.sh again - it sees the existing
# service and keeps its settings.
#
# Graphics drivers are NOT installed by this script (that can break a system);
# if a card is found without a working driver it tells you what to do.
# Re-running the script updates an existing installation and keeps its data.

set -euo pipefail

WHISPER_TAG="v1.9.2"          # must match the version the app expects
MIN_GLIBC="2.34"
PORT=8321
# Where to download QS-PodScript from when the script is not run from the
# unpacked release folder (set once releases are hosted):
QSPODSCRIPT_URL="${QSPODSCRIPT_URL:-${PODSCRIBE_URL:-}}"

INSTALL_DIR="${QSPODSCRIPT_DIR:-${PODSCRIBE_DIR:-$HOME/.local/share/qs-podscript}}"
CPU_ONLY=0
MODEL=""
SERVICE=1
WANT_SERVICE=""     # "" = ask, 1 = yes, 0 = no
WANT_GPU_SPK=""     # "" = ask, 1 = yes, 0 = no
AUTO_ANSWER=""       # "" = ask, y = yes to all, n = no to all, d = defaults
SERVER=""            # "" = desktop install, 1 = server install
EXISTING_SERVER=""
LISTEN=""
TRUSTED=""
TRUSTED_SET=0
ADMIN_NAME=""
SERVER_UNIT="/etc/systemd/system/qs-podscript-server.service"
CONNECT_URL=""
CONNECT_USER=""
CONNECT_LABEL=""
CONNECT_WORK=0

while [ $# -gt 0 ]; do
  case "$1" in
    --dir) INSTALL_DIR="$2"; shift 2 ;;
    --cpu) CPU_ONLY=1; shift ;;
    --model) MODEL="$2"; shift 2 ;;
    --gpu-speakers) WANT_GPU_SPK=1; shift ;;
    --no-gpu-speakers) WANT_GPU_SPK=0; shift ;;
    --service) WANT_SERVICE=1; shift ;;
    --no-service) SERVICE=0; WANT_SERVICE=0; shift ;;
    --yes|-y) AUTO_ANSWER=y; shift ;;
    --no|-n) AUTO_ANSWER=n; shift ;;
    --defaults) AUTO_ANSWER=d; shift ;;
    --server) SERVER=1; shift ;;
    --listen) SERVER=1; LISTEN="$2"; shift 2 ;;
    --trusted-proxy) SERVER=1; TRUSTED="$2"; TRUSTED_SET=1; shift 2 ;;
    --admin) SERVER=1; ADMIN_NAME="$2"; shift 2 ;;
    --connect) CONNECT_URL="$2"; shift 2 ;;
    --connect-user) CONNECT_USER="$2"; shift 2 ;;
    --connect-label) CONNECT_LABEL="$2"; shift 2 ;;
    --connect-work) CONNECT_WORK=1; shift ;;
    --help|-h) awk 'NR == 1 { next } /^#/ { sub(/^# ?/, ""); print; next } { exit }' "$0"; exit 0 ;;
    *) echo "Unknown option: $1 (see --help)"; exit 1 ;;
  esac
done

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# an existing server installation is updated as a server
if [ -z "$SERVER" ] && [ -f "$SERVER_UNIT" ] && grep -q "^ExecStart=$INSTALL_DIR/qs-podscript server" "$SERVER_UNIT"; then
  SERVER=1
  EXISTING_SERVER=1
fi
LOG="/tmp/qs-podscript-install-$$.log"
: > "$LOG"

if [ -t 1 ]; then B=$'\033[1m'; G=$'\033[32m'; Y=$'\033[33m'; R=$'\033[31m'; N=$'\033[0m'; else B=""; G=""; Y=""; R=""; N=""; fi
step() { printf '\n%s==> %s%s\n' "$B" "$*" "$N"; echo "==> $*" >> "$LOG"; }
info() { printf '    %s\n' "$*"; echo "    $*" >> "$LOG"; }
ok()   { printf '    %s✓ %s%s\n' "$G" "$*" "$N"; echo "    OK $*" >> "$LOG"; }
warn() { printf '    %s! %s%s\n' "$Y" "$*" "$N"; echo "    WARN $*" >> "$LOG"; }
die()  { printf '\n%sError: %s%s\n    Details: %s\n' "$R" "$*" "$N" "$LOG"; exit 1; }
run()  { echo "+ $*" >> "$LOG"; "$@" >> "$LOG" 2>&1; }   # quiet, logged

ask() { # ask "question" [default y|n] -> 0 = yes
  local def="${2:-y}" a
  case "$AUTO_ANSWER" in
    y) info "$1 -> yes (-y)"; return 0 ;;
    n) info "$1 -> no (-n)"; return 1 ;;
    d) [ "$def" = y ]; return ;;
  esac
  if [ "$def" = y ]; then
    read -r -p "    $1 [Y/n] " a || true
    [[ -z "$a" || "$a" =~ ^[YyJj] ]]
  else
    read -r -p "    $1 [y/N] " a || true
    [[ "$a" =~ ^[YyJj] ]]
  fi
}

version_ge() { [ "$(printf '%s\n%s\n' "$2" "$1" | sort -V | head -n1)" = "$2" ]; }

# ------------------------------------------------------------------ checks
step "Checking this system"
[ "$(uname -s)" = "Linux" ] || die "This installer is for Linux."
[ "$(uname -m)" = "x86_64" ] || die "Only x86_64 (64-bit PC) is supported so far, this is $(uname -m)."
GLIBC="$(ldd --version 2>/dev/null | head -n1 | grep -oE '[0-9]+\.[0-9]+$' || true)"
if [ -n "$GLIBC" ] && ! version_ge "$GLIBC" "$MIN_GLIBC"; then
  die "Your system is too old (glibc $GLIBC, needs $MIN_GLIBC or newer - e.g. Ubuntu 22.04, Debian 12, Fedora 35)."
fi
ok "Linux x86_64, glibc ${GLIBC:-unknown}"

if [ "$(id -u)" = 0 ]; then
  SUDO=""
  warn "Running as root: QS-PodScript will be installed for root. Better run it as your normal user."
else
  command -v sudo >/dev/null || die "sudo is needed to install packages. Install sudo or run as root."
  SUDO="sudo"
fi

if   command -v apt-get >/dev/null; then PM=apt
elif command -v dnf     >/dev/null; then PM=dnf
elif command -v pacman  >/dev/null; then PM=pacman
elif command -v zypper  >/dev/null; then PM=zypper
else die "No supported package manager found (apt, dnf, pacman, zypper)."; fi
ok "Package manager: $PM"

APT_UPDATED=0
pkg_install() { # pkg_install pkg... -> 0 if all installed
  case "$PM" in
    apt)
      if [ "$APT_UPDATED" = 0 ]; then run $SUDO apt-get update || true; APT_UPDATED=1; fi
      run $SUDO env DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends "$@" ;;
    dnf)    run $SUDO dnf install -y "$@" ;;
    pacman) run $SUDO pacman -S --needed --noconfirm "$@" ;;
    zypper) run $SUDO zypper --non-interactive install "$@" ;;
  esac
}

if [ -n "$SUDO" ]; then
  info "Some steps need administrator rights - you may be asked for your password."
  sudo -v || die "Could not get administrator rights."
fi

# ------------------------------------------------------------------ base packages
step "Installing required programs"
case "$PM" in
  apt)    BASE=(ffmpeg curl ca-certificates tar unzip) ;;
  dnf)    BASE=(curl ca-certificates tar unzip) ;;
  pacman) BASE=(ffmpeg curl ca-certificates tar unzip) ;;
  zypper) BASE=(curl ca-certificates tar unzip) ;;
esac
pkg_install "${BASE[@]}" || die "Installing ${BASE[*]} failed."
if ! command -v ffmpeg >/dev/null; then
  # Fedora ships "ffmpeg-free" (enough for podcasts); full ffmpeg needs RPM Fusion.
  # openSUSE: ffmpeg-7 / ffmpeg-6 depending on release.
  for p in ffmpeg ffmpeg-free ffmpeg-7 ffmpeg-6; do pkg_install "$p" && break || true; done
fi
command -v ffmpeg >/dev/null || die "Could not install ffmpeg. Please install it with your package manager and run this script again."
ok "ffmpeg $(ffmpeg -version | head -n1 | awk '{print $3}')"

# ------------------------------------------------------------------ old name
# Before the rename the app was called "podscribe" and lived in
# ~/.local/share/podscribe. Move that installation over: the data (transcripts,
# people, voice samples, models, GPU build) is kept, old launchers are removed.
OLD_DIR="$HOME/.local/share/podscribe"
HAD_OLD_SERVICE=0
if [ -d "$OLD_DIR" ] && [ "$OLD_DIR" != "$INSTALL_DIR" ]; then
  step "Moving the old 'podscribe' installation to QS-PodScript"
  if [ -f "$HOME/.config/systemd/user/podscribe.service" ]; then
    HAD_OLD_SERVICE=1
    run systemctl --user disable --now podscribe.service || true
  fi
  if pgrep -f "$OLD_DIR/podscribe" >/dev/null 2>&1; then
    die "The old podscribe is still running. Close its window (or stop it), then run this installer again."
  fi
  if [ -d "$OLD_DIR/data" ] && [ ! -e "$INSTALL_DIR/data" ]; then
    mkdir -p "$INSTALL_DIR"
    mv "$OLD_DIR/data" "$INSTALL_DIR/data" || die "Could not move $OLD_DIR/data to $INSTALL_DIR/data."
    [ -f "$OLD_DIR/install.log" ] && mv "$OLD_DIR/install.log" "$INSTALL_DIR/install-podscribe.log" || true
    rm -rf "$OLD_DIR"
    ok "Transcripts, people, models and the GPU build moved to $INSTALL_DIR"
  elif [ -e "$INSTALL_DIR/data" ]; then
    warn "Both $OLD_DIR and $INSTALL_DIR contain data - keeping the new one."
    warn "The old folder is left untouched; delete it yourself once you don't need it."
  else
    rm -rf "$OLD_DIR"   # nothing worth keeping (no data folder)
  fi
  rm -f "$HOME/.config/systemd/user/podscribe.service" "$HOME/.local/share/applications/podscribe.desktop"
  [ -L "$HOME/.local/bin/podscribe" ] && rm -f "$HOME/.local/bin/podscribe"
  command -v systemctl >/dev/null && run systemctl --user daemon-reload || true
  ok "Old menu entry, command and service removed"
fi

# ------------------------------------------------------------------ QS-PodScript files
step "Installing QS-PodScript to $INSTALL_DIR"
SRC=""
if [ -x "$SCRIPT_DIR/qs-podscript" ] && [ -f "$SCRIPT_DIR/libsherpa-onnx-c-api.so" ]; then
  SRC="$SCRIPT_DIR"
elif [ -n "$QSPODSCRIPT_URL" ]; then
  TMPD="$(mktemp -d)"
  info "Downloading $QSPODSCRIPT_URL"
  run curl -fL --retry 3 -o "$TMPD/qs-podscript.tar.gz" "$QSPODSCRIPT_URL" || die "Download failed."
  run tar -C "$TMPD" -xzf "$TMPD/qs-podscript.tar.gz" || die "Unpacking failed."
  SRC="$(dirname "$(find "$TMPD" -name qs-podscript -type f -perm -u+x | head -n1)")"
  [ -n "$SRC" ] || die "Downloaded archive does not contain QS-PodScript."
else
  die "Run this script from the unpacked QS-PodScript folder (next to the QS-PodScript program)."
fi
mkdir -p "$INSTALL_DIR"
if [ "$SRC" != "$INSTALL_DIR" ]; then
  # stop a running instance before replacing the program
  systemctl --user stop qs-podscript.service >/dev/null 2>&1 || true
  if [ "$SERVER" = 1 ] && [ -f "$SERVER_UNIT" ]; then run $SUDO systemctl stop qs-podscript-server.service || true; fi
  for f in qs-podscript libsherpa-onnx-c-api.so libonnxruntime.so onnxruntime-version.txt README.txt install.sh; do
    [ -e "$SRC/$f" ] && cp -f "$SRC/$f" "$INSTALL_DIR/"
  done
fi
chmod +x "$INSTALL_DIR/qs-podscript" "$INSTALL_DIR/install.sh" 2>/dev/null || true
"$INSTALL_DIR/qs-podscript" version >> "$LOG" 2>&1 || die "QS-PodScript does not start on this system."
ok "QS-PodScript $("$INSTALL_DIR/qs-podscript" version | awk '{print $2}') installed"
WDIR="$INSTALL_DIR/data/tools/whisper"

# ------------------------------------------------------------------ graphics card
step "Looking for a graphics card"
HAS_NVIDIA=0; HAS_AMD=0; HAS_INTEL=0
for v in /sys/class/drm/card*/device/vendor; do
  [ -r "$v" ] || continue
  case "$(cat "$v")" in
    0x10de) HAS_NVIDIA=1 ;;
    0x1002) HAS_AMD=1 ;;
    0x8086) HAS_INTEL=1 ;;
  esac
done
NV_OK=0; NV_NAME=""; NV_CC=""; NV_DRIVER_CUDA=""
if command -v nvidia-smi >/dev/null && nvidia-smi >/dev/null 2>&1; then
  NV_OK=1; HAS_NVIDIA=1
  NV_NAME="$(nvidia-smi --query-gpu=name --format=csv,noheader | head -n1)"
  NV_CC="$(nvidia-smi --query-gpu=compute_cap --format=csv,noheader 2>/dev/null | head -n1 | tr -d ' .' || true)"
  # header line: "CUDA Version: 13.0" (older drivers) or "CUDA UMD Version: 13.4" (newer)
  NV_DRIVER_CUDA="$(nvidia-smi | grep -m1 -oE 'CUDA (UMD )?Version: [0-9]+\.[0-9]+' | grep -oE '[0-9]+\.[0-9]+$' || true)"
  if [ -z "$NV_DRIVER_CUDA" ]; then # header changed again: go by the driver version
    NV_DRIVER_VER="$(nvidia-smi --query-gpu=driver_version --format=csv,noheader 2>/dev/null | head -n1 | tr -d ' ' || true)"
    case "${NV_DRIVER_VER%%.*}" in
      ''|*[!0-9]*) ;;
      *) if [ "${NV_DRIVER_VER%%.*}" -ge 580 ]; then NV_DRIVER_CUDA="13.0"
         elif [ "${NV_DRIVER_VER%%.*}" -ge 525 ]; then NV_DRIVER_CUDA="12.0"; fi ;;
    esac
  fi
  ok "NVIDIA $NV_NAME (driver supports CUDA ${NV_DRIVER_CUDA:-?})"
elif [ "$HAS_NVIDIA" = 1 ]; then
  warn "An NVIDIA card is installed, but its driver is not working (nvidia-smi fails)."
  case "$PM" in
    apt)    warn "Ubuntu: 'sudo ubuntu-drivers install', then reboot. Debian: see wiki.debian.org/NvidiaGraphicsDrivers" ;;
    dnf)    warn "Fedora: enable RPM Fusion and install akmod-nvidia, then reboot." ;;
    pacman) warn "Arch: install the 'nvidia' (or nvidia-open) package, then reboot." ;;
    zypper) warn "openSUSE: see en.opensuse.org/SDB:NVIDIA_drivers" ;;
  esac
  warn "Then run this installer again for GPU speed. Continuing for now."
fi
[ "$HAS_AMD" = 1 ] && ok "AMD graphics found"
[ "$HAS_INTEL" = 1 ] && ok "Intel graphics found"
[ "$HAS_NVIDIA$HAS_AMD$HAS_INTEL" = "000" ] && info "No graphics card found - using the processor."

# ------------------------------------------------------------------ GPU build of whisper.cpp
BACKEND="cpu"
mem_avail_gb() { echo $(( $(awk '/MemAvailable/ {print $2}' /proc/meminfo) / 1024 / 1024 )); }

build_whisper() { # build_whisper cuda|vulkan cmake-args...
  local kind="$1"; shift
  # the Vulkan build compiles one ~120 MB generated file that alone needs
  # ~4 GB RAM (measured); CUDA kernels need less but still a lot
  local need=4; [ "$kind" = vulkan ] && need=6
  if [ "$(mem_avail_gb)" -lt "$need" ]; then
    warn "Building the $kind version needs about $need GB of free memory, only $(mem_avail_gb) GB is free."
    warn "Close other programs and run the installer again to use the graphics card."
    return 1
  fi
  local src; src="$(mktemp -d)"
  info "Downloading whisper.cpp $WHISPER_TAG source"
  run curl -fL --retry 3 -o "$src/w.tar.gz" "https://github.com/ggml-org/whisper.cpp/archive/refs/tags/$WHISPER_TAG.tar.gz" || return 1
  run tar -C "$src" -xzf "$src/w.tar.gz" || return 1
  local dir; dir="$(find "$src" -maxdepth 1 -type d -name 'whisper.cpp*' | head -n1)"
  info "Compiling the $kind version - this can take 5-30 minutes, please wait..."
  local t0=$SECONDS
  run cmake -S "$dir" -B "$dir/build" -DCMAKE_BUILD_TYPE=Release -DBUILD_SHARED_LIBS=OFF \
      -DWHISPER_BUILD_TESTS=OFF -DWHISPER_BUILD_SERVER=OFF "$@" || return 1
  # some GPU source files need ~4 GB RAM each to compile: limit parallel jobs
  local memgb jobs
  memgb=$(mem_avail_gb)
  jobs=$(( memgb / 4 )); [ "$jobs" -lt 1 ] && jobs=1
  [ "$jobs" -gt "$(nproc)" ] && jobs=$(nproc)
  info "Using $jobs parallel compile job(s) (${memgb} GB RAM free)"
  run cmake --build "$dir/build" -j"$jobs" --target whisper-cli || return 1
  [ -x "$dir/build/bin/whisper-cli" ] || return 1
  "$dir/build/bin/whisper-cli" --help >> "$LOG" 2>&1 || return 1
  mkdir -p "$WDIR"
  rm -f "$WDIR"/*.so* "$WDIR"/whisper-cli
  cp -f "$dir/build/bin/whisper-cli" "$WDIR/whisper-cli"
  # sha256 ties the marker to this exact binary: if whisper-cli is ever
  # replaced (e.g. by copying another computer's data folder), both QS-PodScript
  # and this installer notice and don't trust the marker any more
  printf 'backend=%s\nversion=%s\nbuilt=%s\nsha256=%s\n' "$kind" "$WHISPER_TAG" "$(date -u +%FT%TZ)" \
    "$(sha256sum "$WDIR/whisper-cli" | cut -d' ' -f1)" > "$WDIR/BUILD_INFO"
  rm -rf "$src"
  ok "$kind version built in $(( (SECONDS - t0) / 60 )) min"
}

build_deps() {
  case "$PM" in
    apt)    pkg_install build-essential cmake ;;
    dnf)    pkg_install gcc-c++ make cmake ;;
    pacman) pkg_install base-devel cmake ;;
    zypper) pkg_install gcc-c++ make cmake ;;
  esac
}

find_nvcc() {
  for n in nvcc /usr/local/cuda/bin/nvcc /opt/cuda/bin/nvcc; do
    if command -v "$n" >/dev/null 2>&1; then command -v "$n"; return 0; fi
  done
  return 1
}

try_cuda() {
  [ "$NV_OK" = 1 ] || return 1
  step "Setting up NVIDIA CUDA"
  local nvcc
  if ! nvcc="$(find_nvcc)"; then
    case "$PM" in
      apt)    info "Installing the CUDA toolkit (large download)"; pkg_install nvidia-cuda-toolkit || true ;;
      pacman) info "Installing the CUDA toolkit (large download, several GB)"; pkg_install cuda || true ;;
      *)      info "No CUDA toolkit in the standard repositories of this distribution." ;;
    esac
    nvcc="$(find_nvcc)" || { warn "CUDA toolkit not available - trying Vulkan instead."; return 1; }
  fi
  local cv; cv="$("$nvcc" --version | grep -oE 'release [0-9]+\.[0-9]+' | awk '{print $2}')"
  info "CUDA toolkit $cv ($nvcc)"
  if ! version_ge "$cv" "12.0"; then
    warn "CUDA toolkit $cv is too old (needs 12.0 or newer) - trying Vulkan instead."; return 1
  fi
  if [ -n "$NV_DRIVER_CUDA" ] && ! version_ge "$NV_DRIVER_CUDA" "$cv"; then
    warn "The NVIDIA driver only supports CUDA $NV_DRIVER_CUDA, the toolkit is $cv - update the driver. Trying Vulkan instead."; return 1
  fi
  build_deps || { warn "Could not install compilers."; return 1; }
  local arch="${NV_CC:-}"; [ -n "$arch" ] || arch="61;70;75;80;86;89"   # only the own card = much faster build
  PATH="$(dirname "$nvcc"):$PATH" build_whisper cuda -DGGML_CUDA=ON -DCMAKE_CUDA_ARCHITECTURES="$arch" \
      -DCMAKE_CUDA_COMPILER="$nvcc" || { warn "CUDA build failed (details in $LOG) - trying Vulkan instead."; return 1; }
  BACKEND=cuda
}

vulkan_gpu_present() { # a real GPU (not the llvmpipe software renderer) visible to Vulkan
  command -v vulkaninfo >/dev/null || return 1
  vulkaninfo --summary 2>/dev/null | grep -i 'deviceType' | grep -qiv 'CPU'
}

try_vulkan() {
  [ "$HAS_NVIDIA$HAS_AMD$HAS_INTEL" != "000" ] || return 1
  [ "$HAS_NVIDIA" = 1 ] && [ "$NV_OK" = 0 ] && [ "$HAS_AMD$HAS_INTEL" = "00" ] && return 1
  step "Setting up Vulkan (graphics card acceleration)"
  case "$PM" in
    apt)    pkg_install vulkan-tools libvulkan-dev glslc spirv-headers || true ;;
    dnf)    pkg_install vulkan-tools vulkan-headers vulkan-loader-devel glslc spirv-headers-devel || true ;;
    pacman) pkg_install vulkan-tools vulkan-headers vulkan-icd-loader shaderc spirv-headers || true ;;
    zypper) pkg_install vulkan-tools vulkan-devel shaderc spirv-headers || true ;;
  esac
  if ! vulkan_gpu_present; then
    warn "No graphics card is usable through Vulkan (driver missing?). Using the processor."; return 1
  fi
  command -v glslc >/dev/null || { warn "Vulkan shader compiler (glslc) not available on this system. Using the processor."; return 1; }
  info "Vulkan device: $(vulkaninfo --summary 2>/dev/null | grep -i 'deviceName' | grep -iv llvmpipe | head -n1 | sed 's/.*= //')"
  build_deps || { warn "Could not install compilers."; return 1; }
  build_whisper vulkan -DGGML_VULKAN=ON || { warn "Vulkan build failed (details in $LOG). Using the processor."; return 1; }
  BACKEND=vulkan
}

existing_build="$(sed -n 's/^backend=//p' "$WDIR/BUILD_INFO" 2>/dev/null || true)"
existing_ver="$(sed -n 's/^version=//p' "$WDIR/BUILD_INFO" 2>/dev/null || true)"
# only keep an existing GPU build if the binary really is one
build_is_real() {
  [ -x "$WDIR/whisper-cli" ] || return 1
  local want; want="$(sed -n 's/^sha256=//p' "$WDIR/BUILD_INFO" 2>/dev/null || true)"
  if [ -n "$want" ] && [ "$(sha256sum "$WDIR/whisper-cli" | cut -d' ' -f1)" != "$want" ]; then return 1; fi
  case "$existing_build" in
    cuda)   ldd "$WDIR/whisper-cli" 2>/dev/null | grep -q 'libcudart' ;;
    vulkan) ldd "$WDIR/whisper-cli" 2>/dev/null | grep -q 'libvulkan' ;;
    *)      return 1 ;;
  esac
}
if [ -n "$existing_build" ] && ! build_is_real; then
  warn "The installed whisper.cpp is not the $existing_build build any more (replaced by another version?) - building it again."
  rm -f "$WDIR/BUILD_INFO" "$WDIR"/*.so* "$WDIR/whisper-cli"
  existing_build=""
fi
if [ "$CPU_ONLY" = 1 ]; then
  rm -f "$WDIR/BUILD_INFO"
  info "--cpu given: using the processor version."
elif [ -n "$existing_build" ] && [ "$existing_ver" = "$WHISPER_TAG" ] && [ -x "$WDIR/whisper-cli" ]; then
  BACKEND="$existing_build"
  ok "Keeping the existing $BACKEND build of whisper.cpp $WHISPER_TAG"
else
  try_cuda || try_vulkan || true
  if [ "$BACKEND" = cpu ]; then
    rm -f "$WDIR/BUILD_INFO"
    info "Transcription will run on the processor (slower)."
  fi
fi

# ------------------------------------------------------------------ models + self-check
if [ -z "$MODEL" ]; then
  if [ "$BACKEND" = cpu ]; then MODEL="turbo-q5"; else MODEL="turbo"; fi
fi
step "Downloading speech and speaker models ($MODEL) and running the self-check"
info "This downloads about 1-2 GB."
SETUP_ARGS=(setup --model "$MODEL")
[ "$CPU_ONLY" = 1 ] && SETUP_ARGS+=(--cpu)
if "$INSTALL_DIR/qs-podscript" "${SETUP_ARGS[@]}" 2>&1 | tee -a "$LOG" | grep -E '^\s+(ok|FAIL|WARN)|All checks passed|ERROR'; then :; fi
if grep -q "All checks passed" "$LOG"; then
  ok "Self-check passed"
else
  die "The self-check failed. The log above and $INSTALL_DIR/data/qs-podscript.log show why."
fi
DEVICE="$(grep -E 'ok +whisper' "$LOG" | tail -n1 | sed -E 's/.*(model load|test passed)\): //')"

# ------------------------------------------------------------------ speaker detection on the graphics card
# Optional (NVIDIA only). QS-PodScript does the work itself (same as Setup ->
# Speaker detection): it downloads sherpa-onnx's GPU build and NVIDIA's CUDA 13
# / cuDNN 9 libraries (pinned versions, checked by SHA-256), puts them in place
# and tests them. Kept in data/tools/onnxruntime-gpu and cuda/; an update that
# brings the normal libraries back is fixed by QS-PodScript at its next start.
GPU_SPK_DIR="$INSTALL_DIR/data/tools/onnxruntime-gpu"
gpu_spk() { "$INSTALL_DIR/qs-podscript" gpu-speakers "$@" 2>&1 | tee -a "$LOG" | grep -m1 '^GPU-SPEAKERS' || true; }
gpu_spk_report() {
  case "$1" in
    "GPU-SPEAKERS OK "*) ok "${1#GPU-SPEAKERS OK }" ;;
    "GPU-SPEAKERS INSTALLED "*) ok "Installed - ${1#GPU-SPEAKERS INSTALLED }" ;;
    "GPU-SPEAKERS UNSUPPORTED "*) warn "Not possible here: ${1#GPU-SPEAKERS UNSUPPORTED }" ;;
    *) warn "${1#GPU-SPEAKERS FAILED }"
       warn "QS-PodScript uses the processor instead. Remove it again with ./install.sh --no-gpu-speakers (or in Setup)." ;;
  esac
}
if [ "$WANT_GPU_SPK" = 0 ]; then
  if [ -d "$GPU_SPK_DIR" ] || [ -d "$INSTALL_DIR/cuda" ]; then
    step "Speaker detection on the graphics card"
    gpu_spk remove >/dev/null; ok "Removed - speaker detection runs on the processor"
  fi
elif [ "$CPU_ONLY" = 1 ]; then
  :
elif [ -d "$GPU_SPK_DIR" ]; then
  step "Speaker detection on the graphics card"
  info "Updating and testing it (downloads only what changed)"
  gpu_spk_report "$(gpu_spk install)"
else
  STATUS="$("$INSTALL_DIR/qs-podscript" gpu-speakers status 2>&1 | grep -m1 '^GPU-SPEAKERS' || true)"
  case "$STATUS" in
    "GPU-SPEAKERS POSSIBLE "*)
      step "Speaker detection on the graphics card"
      GPU_SPK=0
      if [ "$WANT_GPU_SPK" = 1 ]; then GPU_SPK=1; else
        info "Speaker detection (telling voices apart) takes about as long as the transcription on the processor."
        info "On your ${NV_NAME:-NVIDIA card} it is many times faster. This downloads about 1.3 GB of NVIDIA libraries once."
        ask "Use the graphics card for speaker detection too?" y && GPU_SPK=1
      fi
      if [ "$GPU_SPK" = 1 ]; then
        info "Downloading (about 1.3 GB) and testing - this takes a few minutes"
        gpu_spk_report "$(gpu_spk install)"
      else
        info "Speaker detection runs on the processor (add it later: Setup -> Speaker detection, or ./install.sh --gpu-speakers)."
      fi ;;
    *)
      if [ "$WANT_GPU_SPK" = 1 ]; then
        step "Speaker detection on the graphics card"
        gpu_spk_report "${STATUS:-GPU-SPEAKERS FAILED the check did not run}"
      fi ;;
  esac
fi

# ------------------------------------------------------------------ shared server (optional)
# Same as Setup -> "Where you work" -> "Add a server". Asked only when no
# server is saved yet (updates don't ask again) and nobody answers for us.
connect_server() { # connect_server -> 0 if connected
  local pw="${QSPODSCRIPT_PASSWORD:-}" args tries=0
  while :; do
    if [ -z "$pw" ]; then
      [ -t 0 ] || { warn "No password (set QSPODSCRIPT_PASSWORD or run in a terminal)."; return 1; }
      read -r -s -p "    Password for $CONNECT_USER on the server: " pw || true; echo
      [ -n "$pw" ] || { warn "No password given."; return 1; }
    fi
    args=(connect "$CONNECT_URL" --user "$CONNECT_USER")
    [ -n "$CONNECT_LABEL" ] && args+=(--label "$CONNECT_LABEL")
    [ "$CONNECT_WORK" = 1 ] && args+=(--work)
    if OUT="$(QSPODSCRIPT_PASSWORD="$pw" "$INSTALL_DIR/qs-podscript" "${args[@]}" 2>&1)"; then
      echo "+ qs-podscript connect $CONNECT_URL --user $CONNECT_USER (password not logged)" >> "$LOG"
      ok "Connected to $CONNECT_URL as $CONNECT_USER"
      [ "$CONNECT_WORK" = 1 ] && info "\"Start transcribing\" also does this server's episodes (after your own)."
      return 0
    fi
    warn "$(grep -m1 -i 'error' <<<"$OUT" | sed 's/^ERROR: //')"
    pw=""; tries=$((tries + 1))
    if [ -n "${QSPODSCRIPT_PASSWORD:-}" ] || [ ! -t 0 ] || [ "$tries" -ge 3 ]; then return 1; fi
    ask "Try again?" y || return 1
  done
}
if [ "$SERVER" != 1 ]; then
  HAS_SERVERS=1
  SAVED="$("$INSTALL_DIR/qs-podscript" connect list 2>/dev/null || true)"
  case "$SAVED" in *"No servers saved"*) HAS_SERVERS=0 ;; esac
  if [ -n "$CONNECT_URL" ]; then
    step "Connecting to the shared server"
    if [ -z "$CONNECT_USER" ] && [ -t 0 ]; then read -r -p "    Your name (login) on the server: " CONNECT_USER || true; fi
    if [ -z "$CONNECT_USER" ]; then warn "--connect needs --connect-user NAME - skipped."
    else connect_server || warn "Not connected. Add the server later in QS-PodScript: Setup -> Where you work -> Add a server."; fi
  elif [ "$HAS_SERVERS" = 0 ] && [ -z "$AUTO_ANSWER" ] && [ -t 0 ]; then
    step "Shared server (optional)"
    info "If someone runs a shared QS-PodScript server (e.g. for a podcast you help with),"
    info "this computer can connect to it now - to work on its podcasts and help transcribing."
    info "You can also do this later in QS-PodScript: Setup -> Where you work -> Add a server."
    if ask "Connect to a shared server now?" n; then
      read -r -p "    Server address (e.g. https://transcribe.example.org): " CONNECT_URL || true
      read -r -p "    Your name (login) on the server: " CONNECT_USER || true
      read -r -p "    Name for it on this computer (optional, Enter to skip): " CONNECT_LABEL || true
      ask "Transcribe for this server when you press \"Start transcribing\"?" n && CONNECT_WORK=1
      if [ -n "$CONNECT_URL" ] && [ -n "$CONNECT_USER" ]; then
        connect_server || warn "Not connected. Add the server later in QS-PodScript: Setup -> Where you work -> Add a server."
      else
        info "Skipped (address or name missing)."
      fi
    fi
  fi
fi

# ------------------------------------------------------------------ launchers
step "Setting up launchers"
mkdir -p "$HOME/.local/bin"
ln -sf "$INSTALL_DIR/qs-podscript" "$HOME/.local/bin/qs-podscript"
ok "Command 'qs-podscript' available (in ~/.local/bin)"

if [ "$SERVER" = 1 ]; then
# ------------------------------------------------------------------ server
interactive() { [ -t 0 ] && [ -z "$AUTO_ANSWER" ]; }
step "Setting up the server"
# keep the settings of an existing service unless new ones were given
OLD_EXEC="$(sed -n 's/^ExecStart=//p' "$SERVER_UNIT" 2>/dev/null | head -n1)"
if [ -n "$OLD_EXEC" ]; then
  [ -z "$LISTEN" ] && LISTEN="$(sed -nE 's/.*--listen[ =]([^ ]+).*/\1/p' <<<"$OLD_EXEC")"
  [ "$TRUSTED_SET" = 0 ] && TRUSTED="$(sed -nE 's/.*--trusted-proxy[ =]([^ ]+).*/\1/p' <<<"$OLD_EXEC")"
elif [ "$TRUSTED_SET" = 0 ] && interactive; then
  info "Does the reverse proxy (HTTPS) run on another machine or container? Then enter"
  info "its address, so visitors are told apart. Leave empty if it runs on this machine."
  read -r -p "    Address of the reverse proxy: " TRUSTED || true
fi
if [ -z "$LISTEN" ]; then
  if [ -n "$TRUSTED" ]; then LISTEN="0.0.0.0:8322"; else LISTEN="127.0.0.1:8322"; fi
fi
SERVER_ARGS="--listen $LISTEN"
[ -n "$TRUSTED" ] && SERVER_ARGS="$SERVER_ARGS --trusted-proxy $TRUSTED"
ok "Listens on $LISTEN${TRUSTED:+, reverse proxy at $TRUSTED}"

# first admin login
if "$INSTALL_DIR/qs-podscript" user list 2>/dev/null | grep -q "No users yet"; then
  if [ -z "$ADMIN_NAME" ] && interactive; then
    read -r -p "    Name for the first admin login (empty = later): " ADMIN_NAME || true
  fi
  if [ -n "$ADMIN_NAME" ] && [ -t 0 ]; then
    if "$INSTALL_DIR/qs-podscript" user add "$ADMIN_NAME" --admin; then ok "Admin login '$ADMIN_NAME' created"
    else warn "Could not create the admin login - try again with: qs-podscript user add $ADMIN_NAME --admin"; fi
  else
    [ -n "$ADMIN_NAME" ] && warn "Creating a login needs a password typed in a terminal."
    info "No logins yet. Add the first admin with: qs-podscript user add <name> --admin"
  fi
else
  ok "Logins: $("$INSTALL_DIR/qs-podscript" user list 2>/dev/null | wc -l) users"
fi

USE_SERVICE=0
if [ "$SERVICE" = 0 ]; then
  info "--no-service: start the server yourself with: qs-podscript server $SERVER_ARGS"
elif ! command -v systemctl >/dev/null || [ ! -d /run/systemd/system ]; then
  info "No systemd here - start the server with: $INSTALL_DIR/qs-podscript server $SERVER_ARGS"
else
  USE_SERVICE=1
  printf '%s\n' \
    "[Unit]" \
    "Description=QS-PodScript server" \
    "Wants=network-online.target" \
    "After=network-online.target" \
    "" \
    "[Service]" \
    "User=$(id -un)" \
    "ExecStart=$INSTALL_DIR/qs-podscript server $SERVER_ARGS" \
    "Restart=on-failure" \
    "RestartSec=5" \
    "" \
    "[Install]" \
    "WantedBy=multi-user.target" | $SUDO tee "$SERVER_UNIT" >/dev/null
  run $SUDO systemctl daemon-reload
  run $SUDO systemctl enable qs-podscript-server.service || true
  if run $SUDO systemctl restart qs-podscript-server.service; then
    sleep 2
    if systemctl is-active --quiet qs-podscript-server.service; then ok "Service qs-podscript-server running"
    else warn "The service did not stay up - see: journalctl -u qs-podscript-server -n 30"; fi
  else
    warn "Could not start the service - see: journalctl -u qs-podscript-server -n 30"
  fi
fi
printf '%s\n' \
  '#!/usr/bin/env bash' \
  '# Removes the QS-PodScript server including all transcripts, logins and models.' \
  "read -r -p \"Remove the QS-PodScript server and ALL its data in $INSTALL_DIR? [y/N] \" a" \
  '[[ "$a" =~ ^[YyJj] ]] || exit 0' \
  "$SUDO systemctl disable --now qs-podscript-server.service 2>/dev/null || true" \
  "$SUDO rm -f $SERVER_UNIT" \
  "$SUDO systemctl daemon-reload 2>/dev/null || true" \
  'rm -f "$HOME/.local/bin/qs-podscript"' \
  "rm -rf \"$INSTALL_DIR\"" \
  'echo "QS-PodScript removed. (ffmpeg and other system packages were left installed.)"' > "$INSTALL_DIR/uninstall.sh"
chmod +x "$INSTALL_DIR/uninstall.sh"
cp -f "$LOG" "$INSTALL_DIR/install.log" 2>/dev/null || true

step "Done"
[ -n "$EXISTING_SERVER" ] && ok "Existing server updated (settings kept)"
info "Server: QS-PodScript $("$INSTALL_DIR/qs-podscript" version | awk '{print $2}'), listening on $LISTEN"
case "$BACKEND" in
  cuda|vulkan) info "If you let the server transcribe itself, it uses the graphics card ($BACKEND)." ;;
  *)           info "The server doesn't transcribe by itself - helpers' computers do (Setup -> Who transcribes)." ;;
esac
[ "$USE_SERVICE" = 1 ] && info "Stop/start: systemctl stop|start qs-podscript-server    Log: journalctl -u qs-podscript-server"
[ "${LISTEN%%:*}" = "127.0.0.1" ] && info "Put a reverse proxy with HTTPS on this machine in front of http://$LISTEN/ (see README.txt)."
info "Logins:    qs-podscript user list | add <name> [--admin] | passwd <name>"
info "Update:    run the install.sh of a newer version (your data and settings are kept)"
info "Uninstall: $INSTALL_DIR/uninstall.sh"
info "Install log: $INSTALL_DIR/install.log"
exit 0
fi

HAVE_SYSTEMD=0
if [ "$SERVICE" = 1 ] && command -v systemctl >/dev/null && systemctl --user show-environment >/dev/null 2>&1; then
  HAVE_SYSTEMD=1
fi
SERVICE_FILE="$HOME/.config/systemd/user/qs-podscript.service"
# default: no; when updating an installation that already has it: keep it
SERVICE_DEFAULT=n
[ -f "$SERVICE_FILE" ] && SERVICE_DEFAULT=y
[ "$HAD_OLD_SERVICE" = 1 ] && SERVICE_DEFAULT=y   # the old podscribe had it
if [ "$HAVE_SYSTEMD" = 1 ]; then
  case "$WANT_SERVICE" in
    1) USE_SERVICE=1 ;;
    0) USE_SERVICE=0 ;;
    *) if ask "Run QS-PodScript in the background, starting automatically when you log in?" "$SERVICE_DEFAULT"; then USE_SERVICE=1; else USE_SERVICE=0; fi ;;
  esac
else
  USE_SERVICE=0
fi
if [ "$USE_SERVICE" = 1 ]; then
  mkdir -p "$HOME/.config/systemd/user"
  cat > "$HOME/.config/systemd/user/qs-podscript.service" <<EOF
[Unit]
Description=QS-PodScript - podcast transcription
After=network-online.target

[Service]
ExecStart=$INSTALL_DIR/qs-podscript serve --no-browser --port $PORT
Restart=on-failure
RestartSec=10
# transcription is heavy; keep the desktop responsive
Nice=10

[Install]
WantedBy=default.target
EOF
  run systemctl --user daemon-reload
  run systemctl --user enable --now qs-podscript.service || warn "Could not start the service."
  OPEN_CMD="xdg-open http://127.0.0.1:$PORT/"
  TERMINAL=false
  ok "Background service running"
else
  OPEN_CMD="$INSTALL_DIR/qs-podscript serve"
  TERMINAL=true
  if [ -f "$SERVICE_FILE" ] && [ "$HAVE_SYSTEMD" = 1 ]; then
    run systemctl --user disable --now qs-podscript.service || true
    rm -f "$SERVICE_FILE"
    run systemctl --user daemon-reload || true
    info "Background service removed - start QS-PodScript with the menu entry or 'qs-podscript'."
  fi
  [ "$HAVE_SYSTEMD" = 0 ] && [ "$SERVICE" = 1 ] && info "No systemd user session - start QS-PodScript with the menu entry or 'qs-podscript'."
fi
mkdir -p "$HOME/.local/share/applications"
cat > "$HOME/.local/share/applications/qs-podscript.desktop" <<EOF
[Desktop Entry]
Type=Application
Name=QS-PodScript
Comment=Podcast transcription with speaker detection
Exec=$OPEN_CMD
Terminal=$TERMINAL
Categories=AudioVideo;Audio;Utility;
EOF
ok "Menu entry 'qs-podscript' created"

cat > "$INSTALL_DIR/uninstall.sh" <<EOF
#!/usr/bin/env bash
# Removes QS-PodScript including all transcripts and downloaded models.
read -r -p "Remove QS-PodScript and ALL its data in $INSTALL_DIR? [y/N] " a
[[ "\$a" =~ ^[YyJj] ]] || exit 0
systemctl --user disable --now qs-podscript.service 2>/dev/null || true
rm -f "\$HOME/.config/systemd/user/qs-podscript.service" "\$HOME/.local/share/applications/qs-podscript.desktop" "\$HOME/.local/bin/qs-podscript"
systemctl --user daemon-reload 2>/dev/null || true
rm -rf "$INSTALL_DIR"
echo "QS-PodScript removed. (ffmpeg and other system packages were left installed.)"
EOF
chmod +x "$INSTALL_DIR/uninstall.sh"
cp -f "$LOG" "$INSTALL_DIR/install.log" 2>/dev/null || true

# ------------------------------------------------------------------ summary
step "Done"
case "$BACKEND" in
  cuda)   ok "Transcription uses your NVIDIA graphics card (CUDA)" ;;
  vulkan) ok "Transcription uses your graphics card (Vulkan)" ;;
  *)      info "Transcription runs on the processor. A long episode can take about as long as the episode itself, or longer." ;;
esac
[ -n "$DEVICE" ] && info "Self-check: $DEVICE"
if [ -f "$INSTALL_DIR/libonnxruntime_providers_cuda.so" ]; then
  ok "Speaker detection: NVIDIA graphics card (falls back to the processor if it doesn't work)"
else
  info "Speaker detection runs on the processor."
fi
echo
if [ "$TERMINAL" = false ]; then
  info "Open ${B}http://127.0.0.1:$PORT/${N} in your browser (or 'qs-podscript' in the app menu)."
  info "Stop:    systemctl --user stop qs-podscript     Start: systemctl --user start qs-podscript"
  info "To keep transcribing while logged out: sudo loginctl enable-linger $USER"
else
  info "Start QS-PodScript from the app menu or with: qs-podscript"
fi
info "Update:    run the install.sh of a newer version (your data is kept)"
info "Uninstall: $INSTALL_DIR/uninstall.sh"
info "Install log: $INSTALL_DIR/install.log"
