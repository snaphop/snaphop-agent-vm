#!/bin/sh
# Chromium in an agent-vm guest is the build Playwright pins.
#
# There is only one Chromium in the image rather than two. Playwright refuses
# to drive a browser it did not install -- it pins an exact build per release --
# so a distro chromium alongside it would be several hundred megabytes that
# Playwright never touches. Ubuntu settles it anyway: its chromium package is a
# snap stub, and a VM has no snapd.
#
# The browser is searched for rather than hardcoded. Both halves of the path
# move: the directory carries Playwright's build number, and the layout beneath
# it is Playwright's business -- it renamed chrome-linux to chrome-linux64,
# which is exactly the kind of change that should not turn into a guest with no
# browser. Matching the executable by name survives both. chromium-* excludes
# chromium_headless_shell-*, whose binary is a different program.
set -eu

browsers="${PLAYWRIGHT_BROWSERS_PATH:-/opt/ms-playwright}"
chrome="$(find "${browsers}" -type f -name chrome -path '*/chromium-*' 2>/dev/null | sort -V | tail -n1)"

if [ -z "${chrome}" ]; then
    echo "chromium: no browser found under ${browsers}." >&2
    echo "Reinstall it with: playwright install chromium" >&2
    exit 1
fi

exec "${chrome}" "$@"
