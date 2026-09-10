#!/usr/bin/env bash
# Pin templates/distro/agent-tools.nix to an exact nixpkgs revision.
#
# The file ships following a release branch, which is not a pin: two builds a
# week apart install different versions. This resolves the branch to the commit
# it currently points at, prefetches that commit's tarball for its sha256, and
# rewrites the two bindings in agent-tools.nix so a rebuild is reproducible.
#
# Run it on a host with a working nix store, then commit the result and rebuild
# the nix images. Requires nix (for nix-prefetch-url) and git.
set -euo pipefail

nixfile="$(dirname "$0")/../templates/distro/agent-tools.nix"
branch="${1:-nixos-25.05}"

command -v nix-prefetch-url >/dev/null \
  || { echo "pin-nixpkgs: nix-prefetch-url is not installed; this needs a host with nix" >&2; exit 3; }

echo "resolving NixOS/nixpkgs ${branch}..."
rev="$(git ls-remote https://github.com/NixOS/nixpkgs.git "refs/heads/${branch}" | cut -f1)"
[ -n "${rev}" ] || { echo "pin-nixpkgs: no such nixpkgs branch: ${branch}" >&2; exit 1; }

url="https://github.com/NixOS/nixpkgs/archive/${rev}.tar.gz"
echo "prefetching ${url}..."
sha256="$(nix-prefetch-url --unpack --type sha256 "${url}")"

# Both bindings move together: the revision is what is fetched, and the hash is
# what verifies it. Leaving one behind would silently keep the old tree.
sed -i \
  -e "s|^  nixpkgsRef = .*|  nixpkgsRef = \"${rev}\";|" \
  -e "s|^  nixpkgsSha256 = .*|  nixpkgsSha256 = \"${sha256}\";|" \
  "${nixfile}"

echo "pinned ${branch} to ${rev}"
echo "sha256 ${sha256}"
echo
echo "Rebuild the nix images to pick it up:"
echo "  agent-vm image build ubuntu-nix --force"
