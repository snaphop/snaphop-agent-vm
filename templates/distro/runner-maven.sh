#!/bin/sh
# Install the newest Apache Maven release into a runner image.
#
# Runs as root during the image build, after runner-java.sh has installed
# the JDK Maven runs on (ADR-0023). The full image installs Maven through
# mise; a runner image has no mise, so this reads Maven Central's listing
# of apache-maven versions and takes the newest plain X.Y.Z. The listing's
# own release element is not used: it names release candidates too. The
# archive is checked against the SHA-512 published beside it before it is
# extracted.
#
# The version is resolved when the image is built, not pinned here: a
# rebuild picks up a new release. The manifest records only the source
# image's digest, so a rebuild is not reproducible (ADR-0006).
# `agent-vm update` does not move Maven; rebuilding the image does.
#
# Maven is root-owned and readable by every account, in /opt/maven. mvn and
# mvnDebug are linked into /usr/local/bin; mvn resolves that link to find
# its own home, so MAVEN_HOME is not set. The local repository stays in
# each account's own ~/.m2.
set -eu

prefix=${AGENT_VM_RUNNER_PREFIX:-/opt/actions-runner}
repository=https://repo.maven.apache.org/maven2/org/apache/maven/apache-maven
maven_home=/opt/maven

if ! id github-runner >/dev/null 2>&1; then
    echo "runner-maven: the github-runner account is missing; install the runner first" >&2
    exit 1
fi
if [ ! -f "$prefix/.env" ] || ! grep -q '^JAVA_HOME=' "$prefix/.env"; then
    echo "runner-maven: $prefix/.env has no JAVA_HOME; install the JDK first" >&2
    exit 1
fi
for cmd in curl sha512sum tar sort runuser; do
    if ! command -v "$cmd" >/dev/null 2>&1; then
        echo "runner-maven: $cmd is not on PATH" >&2
        exit 1
    fi
done

version=$(curl -fsSL --retry 5 --retry-delay 2 "$repository/maven-metadata.xml" \
    | sed -n 's|.*<version>\([0-9][0-9]*\.[0-9][0-9]*\.[0-9][0-9]*\)</version>.*|\1|p' \
    | sort -V | tail -n 1)
if [ -z "$version" ]; then
    echo "runner-maven: Maven Central listed no apache-maven release" >&2
    exit 1
fi

file=apache-maven-${version}-bin.tar.gz
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
curl -fsSL --retry 5 --retry-delay 2 -o "$tmp/$file" "$repository/$version/$file"
want=$(curl -fsSL --retry 5 --retry-delay 2 "$repository/$version/$file.sha512" | awk '{ print $1 }')
case "$want" in
    '' | *[!0-9a-f]*)
        echo "runner-maven: Maven Central published no SHA-512 for ${file}" >&2
        exit 1
        ;;
esac
printf '%s  %s\n' "$want" "$tmp/$file" | sha512sum -c -

rm -rf "$maven_home"
mkdir -p "$maven_home"
tar -xzf "$tmp/$file" -C "$maven_home" --strip-components=1 --no-same-owner
chmod -R a+rX "$maven_home"
if [ ! -x "$maven_home/bin/mvn" ]; then
    echo "runner-maven: the ${file} archive did not contain Maven" >&2
    exit 1
fi
for tool in mvn mvnDebug; do
    ln -sf "$maven_home/bin/$tool" "/usr/local/bin/$tool"
done

# Run Maven once as the job account, the way a job reaches it: the link in
# /usr/local/bin, JAVA_HOME from .env, and a home of its own.
java_home=$(sed -n 's/^JAVA_HOME=//p' "$prefix/.env" | tail -n 1)
banner=$(runuser -u github-runner -- env -i \
    HOME="$prefix" PATH=/usr/local/bin:/usr/bin:/bin JAVA_HOME="$java_home" \
    mvn -version 2>&1) || {
    echo "runner-maven: mvn -version failed as github-runner:" >&2
    printf '%s\n' "$banner" >&2
    exit 1
}
printf '%s\n' "$banner" | grep -q "Apache Maven ${version} " || {
    echo "runner-maven: mvn is not Maven ${version}:" >&2
    printf '%s\n' "$banner" >&2
    exit 1
}
