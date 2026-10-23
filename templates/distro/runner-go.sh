#!/bin/sh
# Install or update the newest stable Go toolchain and the Go tools on a
# runner image.
#
# Runs as root: once during the image build, after github-runner.sh has
# created the github-runner account (ADR-0023), and again from
# `agent-vm update`, which finds it on PATH as agent-vm-runner-go. The full
# image installs Go through mise; a runner image has no mise, so this asks
# go.dev, the service mise reads too, for the newest stable release and the
# SHA-256 it publishes for the Linux archive. The archive is checked before
# it is extracted.
#
# golangci-lint comes from its own GitHub release, checked against the
# checksums file published with it; its maintainers do not support building
# it with `go install`. govulncheck, goimports, and gotestsum are built from
# their modules' newest versions with the Go just installed.
#
# No version is pinned here; each is resolved when this runs. The manifest
# records only the source image's digest, so a rebuild is not reproducible
# (ADR-0006). Anything already at the newest version is left alone, so a
# guest that is current downloads only the version listings.
#
# Go is root-owned and readable by every account, in /usr/local/go, where
# go.dev's own instructions put it. The tools are in /usr/local/lib/go-tools,
# outside /usr/local/go so a Go update does not remove them. Every command
# is linked into /usr/local/bin, which is on the PATH of a job and of an SSH
# session. Go finds its GOROOT through that link, so nothing is written to
# /etc/environment or the runner's .env. GOPATH, the module cache, and any
# newer toolchain a go.mod asks for (GOTOOLCHAIN is auto, the release's
# default) stay in each account's own ~/go.
set -eu

prefix=${AGENT_VM_RUNNER_PREFIX:-/opt/actions-runner}
go_root=/usr/local/go
tools_bin=/usr/local/lib/go-tools/bin
api='https://go.dev/dl/?mode=json'
golangci_releases=https://github.com/golangci/golangci-lint/releases

# module|package, built in this order with `go install package@version`.
go_tools="golang.org/x/vuln|golang.org/x/vuln/cmd/govulncheck golang.org/x/tools|golang.org/x/tools/cmd/goimports gotest.tools/gotestsum|gotest.tools/gotestsum"

case "$(uname -m)" in
    x86_64) arch=amd64 ;;
    aarch64 | arm64) arch=arm64 ;;
    *)
        echo "runner-go: unsupported architecture $(uname -m)" >&2
        exit 1
        ;;
esac

if ! id github-runner >/dev/null 2>&1; then
    echo "runner-go: the github-runner account is missing; install the runner first" >&2
    exit 1
fi
if [ ! -d "$prefix" ]; then
    echo "runner-go: $prefix is missing; install the runner first" >&2
    exit 1
fi
for cmd in curl jq sha256sum tar runuser; do
    if ! command -v "$cmd" >/dev/null 2>&1; then
        echo "runner-go: $cmd is not on PATH" >&2
        exit 1
    fi
done

scratch=$(mktemp -d)
staged=
trap 'rm -rf "$scratch" ${staged:+"$staged"}' EXIT

# --- Go ---------------------------------------------------------------------

# The listing is newest first and also carries the newest release of the
# previous minor; the first stable entry is the newest release.
release=$(curl -fsSL --retry 5 --retry-delay 2 "$api" \
    | jq -c --arg arch "$arch" \
        '[.[] | select(.stable)][0] | {version, file: ([.files[] | select(.os == "linux" and .arch == $arch and .kind == "archive")][0])}')
version=$(printf '%s' "$release" | jq -r '.version // empty')
file=$(printf '%s' "$release" | jq -r '.file.filename // empty')
want=$(printf '%s' "$release" | jq -r '.file.sha256 // empty')
case "$version" in
    go[0-9]*) ;;
    *)
        echo "runner-go: go.dev returned '${version}' as the newest stable release, which is not a Go version" >&2
        exit 1
        ;;
esac
case "$file" in
    "${version}.linux-${arch}.tar.gz") ;;
    *)
        echo "runner-go: go.dev listed no ${version} archive for linux/${arch}" >&2
        exit 1
        ;;
esac
case "$want" in
    '' | *[!0-9a-f]*)
        echo "runner-go: go.dev published no SHA-256 for ${file}" >&2
        exit 1
        ;;
esac

installed=
if [ -x "$go_root/bin/go" ]; then
    installed=$("$go_root/bin/go" env GOVERSION 2>/dev/null || true)
fi

if [ "$installed" = "$version" ]; then
    echo "runner-go: ${version} is already the newest stable Go"
else
    archive=$scratch/go.tar.gz
    staged=$(mktemp -d "${go_root}.XXXXXX")
    curl -fsSL --retry 5 --retry-delay 2 -o "$archive" "https://go.dev/dl/${file}"
    printf '%s  %s\n' "$want" "$archive" | sha256sum -c -

    # Extracted beside the old toolchain and swapped in, so a failed download
    # or a short archive leaves the toolchain a guest already has.
    tar -xzf "$archive" -C "$staged" --strip-components=1 --no-same-owner
    if [ ! -x "$staged/bin/go" ] || [ ! -x "$staged/bin/gofmt" ]; then
        echo "runner-go: the ${file} archive did not contain a Go toolchain" >&2
        exit 1
    fi
    chmod -R a+rX "$staged"
    chmod 0755 "$staged"
    rm -rf "$go_root"
    mv "$staged" "$go_root"
    staged=
    echo "runner-go: installed ${version}${installed:+, replacing ${installed}}"
fi

for tool in go gofmt; do
    ln -sf "$go_root/bin/$tool" "/usr/local/bin/$tool"
done

mkdir -p "$tools_bin"

# --- golangci-lint ----------------------------------------------------------

# The newest release is where GitHub redirects .../releases/latest; reading
# the redirect stays clear of the API and its rate limit.
lint_url=$(curl -fsSLI --retry 5 --retry-delay 2 -o /dev/null -w '%{url_effective}' "$golangci_releases/latest")
lint_version=${lint_url##*/tag/v}
case "$lint_version" in
    '' | *[!0-9.]* | .* | *.)
        echo "runner-go: $golangci_releases/latest led to '${lint_url}', which names no golangci-lint version" >&2
        exit 1
        ;;
esac

lint_installed=
if [ -x "$tools_bin/golangci-lint" ]; then
    lint_installed=$("$tools_bin/golangci-lint" version --short 2>/dev/null || true)
fi

if [ "$lint_installed" = "$lint_version" ]; then
    echo "runner-go: golangci-lint ${lint_version} is already the newest"
else
    lint_name=golangci-lint-${lint_version}-linux-${arch}
    lint_base=$golangci_releases/download/v${lint_version}
    curl -fsSL --retry 5 --retry-delay 2 -o "$scratch/${lint_name}.tar.gz" "$lint_base/${lint_name}.tar.gz"
    curl -fsSL --retry 5 --retry-delay 2 -o "$scratch/checksums.txt" "$lint_base/golangci-lint-${lint_version}-checksums.txt"
    lint_want=$(awk -v name="${lint_name}.tar.gz" '$2 == name { print $1 }' "$scratch/checksums.txt")
    case "$lint_want" in
        '' | *[!0-9a-f]*)
            echo "runner-go: golangci-lint ${lint_version} published no SHA-256 for ${lint_name}.tar.gz" >&2
            exit 1
            ;;
    esac
    printf '%s  %s\n' "$lint_want" "$scratch/${lint_name}.tar.gz" | sha256sum -c -
    tar -xzf "$scratch/${lint_name}.tar.gz" -C "$scratch" --no-same-owner "${lint_name}/golangci-lint"
    install -m 0755 "$scratch/${lint_name}/golangci-lint" "$tools_bin/golangci-lint"
    echo "runner-go: installed golangci-lint ${lint_version}${lint_installed:+, replacing ${lint_installed}}"
fi

# --- tools built with go install --------------------------------------------

# The module cache and the build cache are scratch. They stay out of the
# image, and a job fills its own ~/go. GOTOOLCHAIN=local builds with the Go
# installed above rather than letting a tool's go.mod fetch another.
build_env="HOME=$scratch GOPATH=$scratch/gopath GOCACHE=$scratch/gocache GOBIN=$tools_bin GOTOOLCHAIN=local"
for entry in $go_tools; do
    module=${entry%%|*}
    package=${entry#*|}
    name=${package##*/}
    # shellcheck disable=SC2086 # build_env is split into env assignments on purpose.
    latest=$(env $build_env "$go_root/bin/go" list -m -f '{{.Version}}' "${module}@latest")
    current=
    if [ -x "$tools_bin/$name" ]; then
        current=$("$go_root/bin/go" version -m "$tools_bin/$name" 2>/dev/null \
            | awk -v module="$module" '$1 == "mod" && $2 == module { print $3 }')
    fi
    if [ "$current" = "$latest" ]; then
        echo "runner-go: ${name} ${latest} is already the newest"
        continue
    fi
    # shellcheck disable=SC2086
    env $build_env "$go_root/bin/go" install "${package}@${latest}"
    echo "runner-go: installed ${name} ${latest}${current:+, replacing ${current}}"
done

chmod -R a+rX "$(dirname "$tools_bin")"
for bin in "$tools_bin"/*; do
    ln -sf "$bin" "/usr/local/bin/$(basename "$bin")"
done

# Run every command once as the job account, the way a job reaches it: the
# links in /usr/local/bin and a home of its own. A tool that installed but
# cannot run looks fine until a job uses it.
check() {
    if ! runuser -u github-runner -- env -i \
        HOME="$prefix" PATH=/usr/local/bin:/usr/bin:/bin \
        "$@" >/dev/null 2>&1; then
        echo "runner-go: '$*' failed as github-runner" >&2
        exit 1
    fi
}
check go version
check gofmt -l "$go_root/src/errors"
check golangci-lint version
check govulncheck -version
check goimports -l "$go_root/src/errors"
check gotestsum --version

reported=$(runuser -u github-runner -- env -i HOME="$prefix" PATH=/usr/local/bin:/usr/bin:/bin go env GOVERSION)
if [ "$reported" != "$version" ]; then
    echo "runner-go: go on PATH reports '${reported}', want ${version}" >&2
    exit 1
fi
