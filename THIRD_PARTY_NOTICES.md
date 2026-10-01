# Third-party software

QS-PodScript itself is licensed under the GNU AGPL-3.0-or-later (see `LICENSE`).
It is built on the following free software. Their licence texts are in the
`licenses/` folder (and in every release package).

## Bundled in the release packages

| Component | Used for | Licence |
|---|---|---|
| [sherpa-onnx](https://github.com/k2-fsa/sherpa-onnx) (Go bindings + C API library) | speaker detection and recognition | Apache-2.0 – `licenses/LICENSE-sherpa-onnx.txt` |
| [ONNX Runtime](https://github.com/microsoft/onnxruntime) (shipped with sherpa-onnx) | runs the speaker models | MIT – `licenses/LICENSE-onnxruntime.txt` |
| [whisper.cpp](https://github.com/ggml-org/whisper.cpp) – Windows Vulkan build of `whisper-cli.exe` (Windows package only) | speech recognition on AMD/Intel/NVIDIA graphics cards | MIT – `licenses/LICENSE-whisper.cpp.txt` |
| [go-sqlite3](https://github.com/mattn/go-sqlite3) with [SQLite](https://sqlite.org) (compiled in) | database, full-text search (FTS5) | MIT – `licenses/LICENSE-go-sqlite3.txt`; SQLite is public domain |
| [Oswald](https://github.com/googlefonts/OswaldFont), [Source Sans 3](https://github.com/adobe-fonts/source-sans) (via Fontsource, compiled in) | fonts of the "QuickSack" look | SIL OFL 1.1 – `licenses/LICENSE-font-*.txt` |

## Downloaded during setup (not part of the packages)

| Component | Source | Licence |
|---|---|---|
| whisper.cpp builds (Windows CPU/CUDA, Linux CPU) | github.com/ggml-org/whisper.cpp releases | MIT |
| Whisper speech models (ggml format) | huggingface.co/ggerganov/whisper.cpp | MIT (OpenAI Whisper) |
| ffmpeg (Windows) | github.com/BtbN/FFmpeg-Builds | GPL-3.0 (separate program, called as a tool) |
| pyannote segmentation model (ONNX, from sherpa-onnx) | github.com/k2-fsa/sherpa-onnx releases | MIT |
| WeSpeaker voice models (ResNet, CAM++) | github.com/k2-fsa/sherpa-onnx releases | Apache-2.0 |
| NVIDIA NeMo TitaNet models | github.com/k2-fsa/sherpa-onnx releases | CC-BY-4.0 |
| 3D-Speaker models (ERes2Net, CAM++) | github.com/k2-fsa/sherpa-onnx releases | Apache-2.0 |
| ONNX Runtime GPU + NVIDIA CUDA/cuDNN libraries (Linux, optional `--gpu-speakers`) | github.com/microsoft/onnxruntime, pypi.org (NVIDIA wheels) | MIT; NVIDIA licence terms |

On Linux and macOS, ffmpeg and (on macOS) whisper.cpp come from the system's
package manager (apt, Homebrew).
