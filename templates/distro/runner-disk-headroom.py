"""Check the self-hosted runner's free disk before a build (snaphop-build-deploy#393).

The runner is persistent (ADR 32), so build cache and images from earlier jobs
stay on its disk. A build that fills it kills the runner's worker mid-job.
This reports where the space is, removes buildx builders a crashed job left
behind and Docker objects nothing uses, all older than 72 hours, with unused
anonymous volumes and orphaned builder state. If that is not enough, it removes
all unused build cache (#426), and fails before anything is built when the disk
is still short.
"""
from datetime import datetime, timedelta, timezone
import os
import re
import shutil
import subprocess
import sys

GIB = 1024 ** 3
AGE = 'until=72h'
STALE = timedelta(hours=72)
# setup-buildx-action names each job's builder builder-<uuid>; buildx names its
# container buildx_buildkit_<builder>0 and its volume after that container.
LEFTOVER_BUILDER = re.compile(r'buildx_buildkit_builder-[0-9a-f-]{36}0')
LEFTOVER_STATE = re.compile(LEFTOVER_BUILDER.pattern + '_state')
PRUNE = (
    ['docker', 'builder', 'prune', '--force', '--filter', AGE],
    ['docker', 'image', 'prune', '--force', '--filter', AGE],
    # Anonymous volumes only. Docker before 23 rejects the filter instead of also removing named ones.
    ['docker', 'volume', 'prune', '--force', '--filter', 'all=false'],
)
# Still short after that: all build cache no build is using, however new (#426). Tagged images stay.
LAST_RESORT = (
    ['docker', 'builder', 'prune', '--all', '--force'],
)
REPORT = (
    ['docker', 'system', 'df'],
    ['docker', 'ps', '--all', '--filter', 'name=buildx_buildkit', '--format', '{{.Names}} {{.CreatedAt}} {{.Status}}'],
)


def watched_paths(env):
    """Directories whose filesystems a job writes to, the runner's own _diag included."""
    paths = []
    workspace = env.get('RUNNER_WORKSPACE')
    if workspace:
        paths.append(os.path.dirname(os.path.dirname(workspace)))
    for name in ('RUNNER_WORKSPACE', 'RUNNER_TEMP', 'RUNNER_TOOL_CACHE'):
        if env.get(name):
            paths.append(env[name])
    docker_root = run(['docker', 'info', '--format', '{{.DockerRootDir}}']).strip()
    if docker_root:
        paths.append(docker_root)
    return paths


def free_bytes(paths):
    """Free bytes per filesystem, keyed by device, for each path this process can see."""
    found = {}
    for path in paths:
        try:
            device = os.stat(path).st_dev
            usage = shutil.disk_usage(path)
        except OSError:
            continue
        found.setdefault(device, (path, usage.free, usage.total))
    return list(found.values())


def run(command):
    try:
        result = subprocess.run(command, capture_output=True, text=True, timeout=600)
    except (OSError, subprocess.TimeoutExpired):
        result = None
    if result is None or result.returncode != 0:
        print(f'::warning::`{" ".join(command[:3])}` failed; its output is not shown.')
        return ''
    return result.stdout


def leftover_builders(now):
    """Buildx containers that a crashed job's post step never removed, older than any job can run."""
    names = run(['docker', 'ps', '--all', '--filter', 'name=buildx_buildkit_builder-', '--format', '{{.Names}}']).split()
    stale = []
    for name in names:
        if not LEFTOVER_BUILDER.fullmatch(name):
            continue
        created = run(['docker', 'inspect', '--format', '{{.Created}}', name]).strip()
        try:
            when = datetime.fromisoformat(re.sub(r'(\.\d{6})\d*', r'\1', created).replace('Z', '+00:00'))
        except ValueError:
            continue
        if now - when > STALE:
            stale.append(name)
    return stale


def remove_builder(name):
    run(['docker', 'rm', '--force', '--volumes', name])
    run(['docker', 'volume', 'rm', f'{name}_state'])


def orphaned_builder_state():
    """State volumes of setup-buildx builders whose container is gone, which no container uses."""
    names = run(['docker', 'volume', 'ls', '--quiet', '--filter', 'dangling=true']).split()
    return [name for name in names if LEFTOVER_STATE.fullmatch(name)]


def free_docker(paths, fail_below, say):
    """Remove unused Docker leftovers, the oldest first; return the filesystems afterwards."""
    for name in leftover_builders(datetime.now(timezone.utc)):
        remove_builder(name)
        say(f'Removed leftover buildx builder `{name}` and its state volume.')
    for name in orphaned_builder_state():
        run(['docker', 'volume', 'rm', name])
        say(f'Removed state volume `{name}`, whose builder is gone.')
    for command in PRUNE:
        output = run(command).strip()
        if output:
            say(f'`{" ".join(command)}`: {output.splitlines()[-1]}')
    filesystems = free_bytes(paths)
    if not filesystems or lowest(filesystems) >= fail_below:
        return filesystems
    for command in LAST_RESORT:
        output = run(command).strip()
        if output:
            say(f'`{" ".join(command)}`: {output.splitlines()[-1]}')
    return free_bytes(paths)


def lowest(filesystems):
    return min(free for _, free, _ in filesystems)


def describe(filesystems):
    return '\n'.join(f'- `{path}`: {free / GIB:.1f} GiB free of {total / GIB:.1f} GiB'
                     for path, free, total in filesystems)


def summary(text):
    print(text)
    target = os.environ.get('GITHUB_STEP_SUMMARY')
    if not target:
        return
    try:
        with open(target, 'a', encoding='utf-8') as handle:
            handle.write(text + '\n')
    except OSError:
        pass


def main(env=os.environ):
    prune_below = float(env.get('RUNNER_DISK_PRUNE_BELOW_GIB', '20')) * GIB
    fail_below = float(env.get('RUNNER_DISK_FAIL_BELOW_GIB', '5')) * GIB
    runner = env.get('RUNNER_NAME', 'this runner')
    paths = watched_paths(env)
    filesystems = free_bytes(paths)
    if not filesystems:
        print(f'::warning::Could not measure free disk on {runner}.')
        return 0
    summary(f'### Disk on {runner}\n\n{describe(filesystems)}')
    for command in REPORT:
        output = run(command).strip()
        if output:
            summary(f'\n`{" ".join(command[:3])}`\n\n```\n{output}\n```')
    if lowest(filesystems) >= prune_below:
        return 0
    filesystems = free_docker(paths, fail_below, lambda text: summary(f'\n{text}'))
    summary(f'\nAfter pruning:\n\n{describe(filesystems)}')
    free = lowest(filesystems)
    if free >= fail_below:
        return 0
    print(f'::error::{runner} has {free / GIB:.1f} GiB free, below the {fail_below / GIB:.0f} GiB a build needs. '
          'Nothing was built or published. Free disk on that runner, or take it offline, then press the workflow again.')
    return 1


if __name__ == '__main__':
    sys.exit(main())
