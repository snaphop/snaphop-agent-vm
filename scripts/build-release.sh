#!/usr/bin/env bash
# Build the release artifact: one static agent-vm binary per architecture.
#
# There is no container image and no server — installation is copying the binary
# onto a KVM-capable host — so this script is the whole packaging step. It builds
# with CGO disabled because the tool is pure Go and talks to libvirt through
# virt-install and virsh, never through the C bindings.
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
    -ldflags "-s -w -X git.snaphop.xyz/snaphop/snaphop-agent-vm/internal/cli.Version=$VERSION" \
    -o "$output" \
    ./cmd/agent-vm
done

echo
echo "built into $OUT_DIR:"
ls -l "$OUT_DIR"
