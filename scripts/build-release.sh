#!/usr/bin/env bash
# Build the release artifact: one static agent-vm binary per architecture,
# plus the license notices that have to sit beside those binaries.
#
# There is no container image and no server — installation is copying the binary
# onto a KVM-capable host — so this script is the whole packaging step. It builds
# with CGO disabled because the tool is pure Go and talks to libvirt through
# virt-install and virsh, never through the C bindings. The same notices are
# embedded in the binary; the copies here are what a release attaches for
# someone who keeps the files next to the program.
set -euo pipefail

cd "$(dirname "$0")/.."

VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
OUT_DIR="${OUT_DIR:-dist}"
ARCHES="${ARCHES:-amd64 arm64}"

mkdir -p "$OUT_DIR"

for arch in $ARCHES; do
  output="$OUT_DIR/agent-vm-linux-$arch"
  echo "building $output ($VERSION)"

  CGO_ENABLED=0 GOOS=linux GOARCH="$arch" \
    go build \
    -trimpath \
    -ldflags "-s -w -X github.com/snaphop/snaphop-agent-vm/internal/cli.Version=$VERSION" \
    -o "$output" \
    ./cmd/agent-vm
done

# LICENSE is this project's MIT license. NOTICE carries the copyright and
# permission notices of the code linked into the binary. Both already live at
# the repository root; a release directory needs them next to the binaries.
cp LICENSE NOTICE "$OUT_DIR/"

echo
echo "built into $OUT_DIR:"
ls -l "$OUT_DIR"
