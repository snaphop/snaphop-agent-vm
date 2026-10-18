"""Clean the self-hosted runner at the start of every job (snaphop-build-deploy#426).

The runner runs this through its job-started hook, so it covers every job of
every repository, Verify included, not only the five deploys that run the
runner-disk action. The owner installs it outside the runner's directory, where
a job cannot change it; see README.md.

It removes, without asking:

- `_work/<repository>` checkouts that no job has touched for 72 hours, never
  the one this job uses;
- the runner's own `_diag` logs older than seven days;
- below the prune threshold, the same Docker leftovers the runner-disk action
  removes, with the same last resort.

It never stops the job. A cleanup that fails only prints a warning.
"""
import os
from pathlib import Path
import shutil
import sys
import time

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import headroom  # noqa: E402

UNUSED_CHECKOUT = 72 * 3600
OLD_DIAG = 7 * 24 * 3600


def runner_root(env):
    """The runner's install folder, from the hook's .env or from the job's workspace."""
    root = env.get('SNAPHOP_RUNNER_ROOT')
    if not root and env.get('RUNNER_WORKSPACE'):
        root = os.path.dirname(os.path.dirname(env['RUNNER_WORKSPACE']))
    if not root:
        return None
    root = Path(root)
    return root if (root / '_work').is_dir() and not root.is_symlink() else None


def last_touched(path, depth=2):
    """Newest mtime of a folder and its entries up to depth levels down, symlinks not followed."""
    newest = path.lstat().st_mtime
    if depth == 0:
        return newest
    try:
        entries = list(os.scandir(path))
    except OSError:
        return newest
    for entry in entries:
        try:
            if entry.is_dir(follow_symlinks=False):
                newest = max(newest, last_touched(Path(entry.path), depth - 1))
            else:
                newest = max(newest, entry.stat(follow_symlinks=False).st_mtime)
        except OSError:
            continue
    return newest


def unused_checkouts(root, repository, now):
    """_work/<name> folders other than this job's that no job has touched for 72 hours."""
    if not repository:
        return []
    current = repository.rsplit('/', 1)[-1]
    work = root / '_work'
    unused = []
    for entry in sorted(work.iterdir()):
        # The runner's own folders, such as _actions, _tool and _temp, start with an underscore.
        if entry.name.startswith('_') or entry.name == current:
            continue
        if entry.is_symlink() or not entry.is_dir():
            continue
        if now - last_touched(entry) > UNUSED_CHECKOUT:
            unused.append(entry)
    return unused


def old_diag_logs(root, now):
    diag = root / '_diag'
    if not diag.is_dir() or diag.is_symlink():
        return []
    return [log for log in sorted(diag.glob('*.log'))
            if log.is_file() and not log.is_symlink() and now - log.stat().st_mtime > OLD_DIAG]


def remove(path):
    try:
        if path.is_dir() and not path.is_symlink():
            shutil.rmtree(path)
        else:
            path.unlink()
    except OSError as error:
        print(f'::warning::Could not remove {path.name}: {error.strerror}.')
        return False
    return True


def clean_files(root, env, now):
    removed = 0
    for checkout in unused_checkouts(root, env.get('GITHUB_REPOSITORY', ''), now):
        if remove(checkout):
            removed += 1
            print(f'Removed checkout _work/{checkout.name}, unused for over 72 hours.')
    logs = [log for log in old_diag_logs(root, now) if remove(log)]
    if logs:
        print(f'Removed {len(logs)} runner log(s) older than seven days from _diag.')
    return removed + len(logs)


def clean_docker(env):
    prune_below = float(env.get('RUNNER_DISK_PRUNE_BELOW_GIB', '20')) * headroom.GIB
    fail_below = float(env.get('RUNNER_DISK_FAIL_BELOW_GIB', '5')) * headroom.GIB
    paths = headroom.watched_paths(env)
    filesystems = headroom.free_bytes(paths)
    if not filesystems or headroom.lowest(filesystems) >= prune_below:
        return
    headroom.free_docker(paths, fail_below, print)


def main(env=os.environ, now=None):
    now = time.time() if now is None else now
    runner = env.get('RUNNER_NAME', 'this runner')
    root = runner_root(env)
    if root is None:
        print(f'::warning::Runner cleanup skipped files on {runner}: set SNAPHOP_RUNNER_ROOT in the runner\'s .env.')
    else:
        env = {**env, 'RUNNER_WORKSPACE': env.get('RUNNER_WORKSPACE') or str(root / '_work' / 'x')}
        clean_files(root, env, now)
    clean_docker(env)
    filesystems = headroom.free_bytes(headroom.watched_paths(env))
    if filesystems:
        print(f'Disk on {runner} after cleanup:\n{headroom.describe(filesystems)}')
    return 0


if __name__ == '__main__':
    try:
        main()
    except Exception as error:  # A cleanup bug must never fail the job.
        print(f'::warning::Runner cleanup stopped early: {type(error).__name__}.')
    sys.exit(0)
