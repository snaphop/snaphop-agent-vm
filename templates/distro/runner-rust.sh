#!/bin/sh
# Install Rust and the pinned cargo tools into a runner image.
#
# Runs as root during the image build, after github-runner.sh has created
# the github-runner account and installed the C toolchain and OpenSSL
# headers the cargo tools below compile against (ADR-0022).
#
# rustup goes into the same shared /usr/local/rustup and /usr/local/cargo
# the full image uses, so `agent-vm update` moves it with the step it
# already runs there. The latest stable toolchain is the default, with
# llvm-tools for cargo-llvm-cov and the wasm32-unknown-unknown target for
# worker-build. The latest nightly is installed beside it, because cargo-fuzz
# runs only on nightly. Neither toolchain is pinned: the manifest records
# only the source image's digest, so a rebuild is not reproducible
# (ADR-0006).
#
# RUSTUP_HOME is owned by github-runner. A workflow step that runs
# `rustup toolchain install` or `rustup component add` then works without
# sudo, as it does on a GitHub-hosted runner. The agent account reads it.
# RUSTUP_HOME goes into /etc/environment for SSH sessions and into the
# runner's .env for jobs. CARGO_HOME is left unset for both, so
# `cargo install` writes into that account's own ~/.cargo.
#
# The cargo tools are pinned and built from crates.io with --locked into
# /usr/local/cargo/bin. A rebuild of the image is how a pin moves.
set -eu

prefix=${AGENT_VM_RUNNER_PREFIX:-/opt/actions-runner}
rustup_home=/usr/local/rustup
cargo_home=/usr/local/cargo

# crate@version, installed in this order.
tools="cargo-llvm-cov@0.9.1 cargo-audit@0.22.1 cargo-deny@0.20.2 cargo-fuzz@0.13.2 worker-build@0.8.7"

if ! id github-runner >/dev/null 2>&1; then
    echo "runner-rust: the github-runner account is missing; install the runner first" >&2
    exit 1
fi
if [ ! -d "$prefix" ]; then
    echo "runner-rust: $prefix is missing; install the runner first" >&2
    exit 1
fi
for cmd in curl cc pkg-config; do
    if ! command -v "$cmd" >/dev/null 2>&1; then
        echo "runner-rust: $cmd is not on PATH" >&2
        exit 1
    fi
done

export RUSTUP_HOME="$rustup_home" CARGO_HOME="$cargo_home"
curl -fsSL --retry 5 --retry-delay 2 https://sh.rustup.rs \
    | sh -s -- -y --no-modify-path --profile default --default-toolchain stable
PATH="$cargo_home/bin:$PATH"
export PATH

rustup component add --toolchain stable llvm-tools
rustup target add --toolchain stable wasm32-unknown-unknown
rustup toolchain install nightly --profile minimal

# The registry and the build directory are scratch. They stay out of the
# image, and a job fills its own ~/.cargo registry.
build=$(mktemp -d)
trap 'rm -rf "$build"' EXIT
for tool in $tools; do
    crate=${tool%@*}
    version=${tool#*@}
    CARGO_HOME="$build/home" CARGO_TARGET_DIR="$build/target" \
        cargo install --locked --root "$cargo_home" --version "$version" "$crate"
done

for bin in "$cargo_home"/bin/*; do
    ln -sf "$bin" "/usr/local/bin/$(basename "$bin")"
done

chmod -R a+rX "$rustup_home" "$cargo_home"
chown -R github-runner:github-runner "$rustup_home"

# set_env_line replaces one KEY=value line in a file and keeps the others.
set_env_line() {
    file=$1
    key=$2
    value=$3
    lines=$(mktemp)
    if [ -f "$file" ]; then
        grep -v "^${key}=" "$file" > "$lines" || true
    fi
    printf '%s=%s\n' "$key" "$value" >> "$lines"
    cat "$lines" > "$file"
    rm -f "$lines"
}

set_env_line /etc/environment RUSTUP_HOME "$rustup_home"
set_env_line "$prefix/.env" RUSTUP_HOME "$rustup_home"
chown github-runner:github-runner "$prefix/.env"
chmod 0644 "$prefix/.env"

# Run everything once as the job account, the way a job reaches it: the
# links in /usr/local/bin, RUSTUP_HOME from .env, and a home of its own. A
# toolchain that installed but cannot run looks fine until a job uses it.
check() {
    if ! runuser -u github-runner -- env -i \
        HOME="$prefix" PATH=/usr/local/bin:/usr/bin:/bin RUSTUP_HOME="$rustup_home" \
        "$@" >/dev/null 2>&1; then
        echo "runner-rust: '$*' failed as github-runner" >&2
        exit 1
    fi
}
check rustc --version
check cargo --version
check rustup --version
check cargo fmt --version
check cargo clippy --version
check rustc +nightly --version
check cargo llvm-cov --version
check cargo audit --version
check cargo deny --version
check cargo fuzz --version
check worker-build --version

installed=$(cargo install --list --root "$cargo_home")
for tool in $tools; do
    crate=${tool%@*}
    version=${tool#*@}
    if ! printf '%s\n' "$installed" | grep -qx "${crate} v${version}:"; then
        echo "runner-rust: ${crate} ${version} is not the version installed" >&2
        exit 1
    fi
done
