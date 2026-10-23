#!/bin/sh
# Install the current Eclipse Temurin JDK LTS into a runner image.
#
# Runs as root during the image build, after github-runner.sh has created
# the runner directory (ADR-0022). Temurin is the JDK the full image ships.
# The full image installs it through mise; a runner image has no mise, so
# this asks Adoptium's API, the service mise reads too, for the newest LTS
# major and that major's newest build. The archive is checked against the
# SHA-256 the API publishes for it before it is extracted.
#
# The major is resolved when the image is built, not pinned here: a rebuild
# picks up a new LTS. The manifest records only the source image's digest,
# so a rebuild is not reproducible (ADR-0006). `agent-vm update` does not
# move this JDK; rebuilding the image does.
#
# The JDK is root-owned and readable by every account. Its commands are
# linked into /usr/local/bin, which is on the PATH of a job and of an SSH
# session. JAVA_HOME goes into /etc/environment for SSH sessions and into
# the runner's .env for jobs. config.sh keeps a JAVA_HOME already in .env.
set -eu

prefix=${AGENT_VM_RUNNER_PREFIX:-/opt/actions-runner}
api=https://api.adoptium.net/v3
jvm_root=/usr/lib/jvm

case "$(uname -m)" in
    x86_64) arch=x64 ;;
    aarch64 | arm64) arch=aarch64 ;;
    *)
        echo "runner-java: unsupported architecture $(uname -m)" >&2
        exit 1
        ;;
esac

if [ ! -d "$prefix" ]; then
    echo "runner-java: $prefix is missing; install the runner first" >&2
    exit 1
fi
for cmd in curl jq sha256sum tar; do
    if ! command -v "$cmd" >/dev/null 2>&1; then
        echo "runner-java: $cmd is not on PATH" >&2
        exit 1
    fi
done

major=$(curl -fsSL --retry 5 --retry-delay 2 "$api/info/available_releases" | jq -r '.most_recent_lts')
case "$major" in
    '' | *[!0-9]*)
        echo "runner-java: Adoptium returned '${major}' as the most recent LTS, which is not a major version" >&2
        exit 1
        ;;
esac

assets=$(curl -fsSL --retry 5 --retry-delay 2 \
    "$api/assets/latest/${major}/hotspot?architecture=${arch}&image_type=jdk&os=linux&vendor=eclipse")
link=$(printf '%s' "$assets" | jq -r '[.[] | select(.binary.image_type == "jdk")][0].binary.package.link // empty')
want=$(printf '%s' "$assets" | jq -r '[.[] | select(.binary.image_type == "jdk")][0].binary.package.checksum // empty')
release=$(printf '%s' "$assets" | jq -r '[.[] | select(.binary.image_type == "jdk")][0].release_name // empty')
case "$link" in
    https://*) ;;
    *)
        echo "runner-java: Adoptium listed no Temurin ${major} JDK for linux/${arch}" >&2
        exit 1
        ;;
esac
case "$want" in
    '' | *[!0-9a-f]*)
        echo "runner-java: Adoptium published no SHA-256 for ${link}" >&2
        exit 1
        ;;
esac

tmp=$(mktemp)
trap 'rm -f "$tmp"' EXIT
curl -fsSL --retry 5 --retry-delay 2 -o "$tmp" "$link"
printf '%s  %s\n' "$want" "$tmp" | sha256sum -c -

java_home=$jvm_root/temurin-${major}
rm -rf "$java_home"
mkdir -p "$java_home"
tar -xzf "$tmp" -C "$java_home" --strip-components=1 --no-same-owner
chmod -R a+rX "$java_home"
if [ ! -x "$java_home/bin/java" ] || [ ! -x "$java_home/bin/javac" ]; then
    echo "runner-java: the ${release} archive did not contain a JDK" >&2
    exit 1
fi

for tool in "$java_home"/bin/*; do
    ln -sf "$tool" "/usr/local/bin/$(basename "$tool")"
done

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

set_env_line /etc/environment JAVA_HOME "$java_home"
set_env_line "$prefix/.env" JAVA_HOME "$java_home"
chown github-runner:github-runner "$prefix/.env"
chmod 0644 "$prefix/.env"

# A JDK that unpacked but cannot start looks fine until a job runs it.
banner=$(java -version 2>&1) || {
    echo "runner-java: java is installed but cannot run" >&2
    exit 1
}
printf '%s\n' "$banner" | grep -q Temurin || {
    echo "runner-java: java is not Eclipse Temurin:" >&2
    printf '%s\n' "$banner" >&2
    exit 1
}
printf '%s\n' "$banner" | grep -q LTS || {
    echo "runner-java: java is not an LTS release:" >&2
    printf '%s\n' "$banner" >&2
    exit 1
}
javac -version >/dev/null 2>&1 || {
    echo "runner-java: javac is installed but cannot run" >&2
    exit 1
}
