#!/bin/sh
# Builds QS-PodScript for Linux (native) and Windows (cross-compiled with MinGW)
# and packages the Windows version as a zip.
# Needs: Go >= 1.22, gcc, patchelf, x86_64-w64-mingw32-gcc (apt install gcc-mingw-w64-x86-64 patchelf)
set -e
cd "$(dirname "$0")"
VERSION=$(grep 'const version' main.go | cut -d'"' -f2)
export GOFLAGS=-mod=mod
rm -rf dist/linux dist/win
mkdir -p dist

echo "== linux"
# $ORIGIN: find the sherpa libraries next to the binary (portable folder)
go build -trimpath -tags sqlite_fts5 -ldflags "-s -w -extldflags '-Wl,-rpath,\$ORIGIN'" -o dist/linux/qs-podscript .
# the sherpa-onnx Go package adds the build machine's module folder to the
# library search path, ahead of $ORIGIN - on a computer where that folder
# exists, the CPU-only library from there would win over the graphics-card
# library the installer puts next to the program. Keep only $ORIGIN.
if command -v patchelf >/dev/null; then
  patchelf --set-rpath '$ORIGIN' dist/linux/qs-podscript
else
  echo "WARNING: patchelf missing (apt install patchelf) - library path still contains the build folder"
fi
LSHERPA=$(go env GOMODCACHE)/github.com/k2-fsa/sherpa-onnx-go-linux@$(go list -m -f '{{.Version}}' github.com/k2-fsa/sherpa-onnx-go-linux)/lib/x86_64-unknown-linux-gnu
cp "$LSHERPA/libsherpa-onnx-c-api.so" "$LSHERPA/libonnxruntime.so" dist/linux/
chmod 644 dist/linux/*.so
# the installer's optional graphics-card part must fetch the same ONNX Runtime version
strings dist/linux/libonnxruntime.so | grep -m1 -oE '^libonnxruntime\.so\.[0-9]+\.[0-9]+\.[0-9]+$' | sed 's/libonnxruntime\.so\.//' > dist/linux/onnxruntime-version.txt
[ -s dist/linux/onnxruntime-version.txt ] || { echo "could not read the ONNX Runtime version"; exit 1; }
cp README-linux.txt dist/linux/README.txt
cp LICENSE THIRD_PARTY_NOTICES.md dist/linux/ && cp -r licenses dist/linux/
cp install.sh dist/linux/install.sh && chmod +x dist/linux/install.sh
tar -C dist -czf dist/qs-podscript-$VERSION-linux-x64.tar.gz --transform "s,^linux,qs-podscript-$VERSION," linux

echo "== windows"
CGO_ENABLED=1 GOOS=windows GOARCH=amd64 CC=x86_64-w64-mingw32-gcc CXX=x86_64-w64-mingw32-g++ \
  go build -trimpath -tags sqlite_fts5 -ldflags "-s -w" -o dist/win/qs-podscript.exe .
SHERPA=$(go env GOMODCACHE)/github.com/k2-fsa/sherpa-onnx-go-windows@$(go list -m -f '{{.Version}}' github.com/k2-fsa/sherpa-onnx-go-windows)/lib/x86_64-pc-windows-gnu
cp "$SHERPA/sherpa-onnx-c-api.dll" "$SHERPA/onnxruntime.dll" dist/win/
chmod 644 dist/win/*.dll
cp README-windows.txt dist/win/README.txt
cp LICENSE THIRD_PARTY_NOTICES.md dist/win/ && cp -r licenses dist/win/
cp install.ps1 install.cmd dist/win/
if [ -f third_party/whisper-vulkan-win64/whisper-cli.exe ]; then
  mkdir -p dist/win/gpu/vulkan
  cp third_party/whisper-vulkan-win64/whisper-cli.exe dist/win/gpu/vulkan/
else
  echo "WARNING: no Vulkan whisper-cli.exe in third_party/ - Windows package without Vulkan"
fi
rm -rf dist/win/data; (cd dist/win && rm -f ../qs-podscript-$VERSION-windows-x64.zip && zip -q -r ../qs-podscript-$VERSION-windows-x64.zip .)
ls -la dist
