#!/bin/sh
# Builds QS-PodScript for macOS on a Mac (or the GitHub Actions macOS runner)
# and packages it as a zip. Needs: Go >= 1.24, Xcode command line tools.
# Apple Silicon by default; ARCH=amd64 ./build-macos.sh for Intel Macs.
set -e
cd "$(dirname "$0")"
VERSION=$(grep 'const version' main.go | cut -d'"' -f2)
ARCH=${ARCH:-arm64}
case "$ARCH" in
  arm64) LIBARCH=aarch64-apple-darwin ;;
  amd64) LIBARCH=x86_64-apple-darwin ;;
  *) echo "unknown ARCH $ARCH"; exit 1 ;;
esac
export GOFLAGS=-mod=mod CGO_ENABLED=1 GOOS=darwin GOARCH=$ARCH
NAME=qs-podscript-$VERSION-macos-$ARCH
OUT=dist/$NAME
rm -rf "$OUT" "dist/$NAME.zip"
mkdir -p "$OUT"

echo "== macOS $ARCH"
# @executable_path: find the sherpa-onnx libraries next to the program
go build -trimpath -tags sqlite_fts5 -ldflags "-s -w -extldflags '-Wl,-rpath,@executable_path'" -o "$OUT/qs-podscript" .
MOD=$(go list -m -f '{{.Dir}}' github.com/k2-fsa/sherpa-onnx-go-macos)
cp "$MOD/lib/$LIBARCH/libsherpa-onnx-c-api.dylib" "$MOD/lib/$LIBARCH/libonnxruntime.dylib" "$OUT/"
chmod 644 "$OUT"/*.dylib
# ad-hoc signatures (Apple Silicon refuses to run unsigned code)
codesign --force --sign - "$OUT"/*.dylib "$OUT/qs-podscript"
cp README-macos.txt "$OUT/README.txt"
cp LICENSE THIRD_PARTY_NOTICES.md "$OUT/" && cp -r licenses "$OUT/"
cp QS-PodScript.command "$OUT/" && chmod +x "$OUT/QS-PodScript.command"
# ditto keeps permissions and signatures intact
(cd dist && ditto -c -k --keepParent "$NAME" "$NAME.zip")
ls -la dist/*.zip
