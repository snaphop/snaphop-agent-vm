#!/bin/bash
# The runner's job-started hook (snaphop-build-deploy#426). The runner accepts
# only a .sh or .ps1 hook, so this starts job-started.py from the same folder.
# A cleanup problem never fails the job.
python3 "$(dirname "$(readlink -f "$0")")/job-started.py" || true
exit 0
