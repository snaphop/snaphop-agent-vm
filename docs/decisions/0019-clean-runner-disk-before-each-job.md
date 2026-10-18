# 19. Clean runner disk before each job

Date: 2026-10-17

## Status

Accepted

Extends [ADR-0013](./0013-self-hosted-github-actions-runner-variant.md) and
[ADR-0016](./0016-install-docker-on-the-runner-image.md). The runner variant
stays the slim recipe plus one shared section. That section also installs
the job-started disk hook.

## Context

A runner VM keeps its disk between jobs. Checkouts under
`/opt/actions-runner/_work`, `_diag` logs, and Docker build cache from
earlier jobs stay on that disk. A build that fills the disk kills the
runner's worker partway through the job.

snaphop-build-deploy carries the cleanup that its self-hosted runners
already run, in `.github/actions/runner-disk`. `job-started.sh` is the
runner's job-started hook. It removes checkouts no job has touched for 72
hours, `_diag` logs older than seven days, and, below 20 GiB free, the same
unused Docker data. It never fails the job. snaphop-ai-stack's
`ops/scripts/install-runner-hook.sh` installs those three files on a runner
that is already registered, from commit
`c68e4b2821de81f4d599fdbb48b162097e90b046`, into
`/usr/local/libexec/snaphop-runner/`, and adds two lines to the runner's
`.env`.

That install happens by hand, over SSH, after the runner exists. A VM
created from a runner image does not have it. The image build cannot clone
snaphop-build-deploy: that repository is private, and a base image build
has no GitHub credential (SECURITY.md).

GitHub's runner reads `ACTIONS_RUNNER_HOOK_JOB_STARTED` from
`/opt/actions-runner/.env` when the service starts. The file has to be in
place before `svc.sh start`. `config.sh` keeps an existing `.env` and may
append to it. The hook programs have to live outside the runner directory.
A job runs as `github-runner` and can write there; a hook inside `_work`
would be whatever the previous job left behind.

## Decision

- The shared runner section installs the hook after the runner account,
  git, and Docker exist, from `templates/distro/runner-cleanup.sh`. The
  script is the same on Ubuntu, Fedora, and Arch.
- The three programs are the copies of `headroom.py`, `job-started.py`, and
  `job-started.sh` from that pinned commit. They are stored in this
  repository. The image build does not fetch them.
- They are installed at `/usr/local/libexec/snaphop-runner/`, owned by
  root. The directory and `job-started.sh` are mode `0755`. The Python
  files are mode `0644`.
- `/opt/actions-runner/.env` gains:

  ```
  ACTIONS_RUNNER_HOOK_JOB_STARTED=/usr/local/libexec/snaphop-runner/job-started.sh
  SNAPHOP_RUNNER_ROOT=/opt/actions-runner
  ```

  The file is mode `0644`, owned by `github-runner`, so `config.sh` can
  append to it. Other lines already in the file stay.
- The build fails when `python3` or `docker` is missing, when the hook is
  not outside the runner directory, or when the hook does not exit 0. The
  slim recipe already installs Python 3. The probe during the build does
  not delete Docker data.
- `agent-vm-github-runner configure` writes the two lines again after
  `config.sh` and before `svc.sh start`, without dropping lines `config.sh`
  added. `remove` does not change `.env`.
- The hook's own rules are unchanged: 72 hours for a checkout, seven days
  for a log, 20 GiB before Docker cleanup, 5 GiB before removing the rest
  of the unused build cache. The current job's checkout, and directories
  whose names start with `_`, stay. A cleanup problem does not fail the
  job.
- No new host tool, no CLI flag, and no schema change. A cached runner
  image gains the hook when it is rebuilt with
  `agent-vm image build <family>-runner --force`. Create the VM again from
  that image.

## Consequences

Easier:

- Every job on a runner VM starts with the same disk cleanup the
  organization already runs on its other self-hosted runners, including
  jobs that never call the runner-disk action.
- The hook is in the image, so a new VM does not need a later SSH install.

Harder:

- A checkout left untouched for 72 hours is deleted. The tool cache and
  downloaded actions, which live under `_work` names that start with `_`,
  stay.
- `github-runner` owns `.env` and can change the two lines. The hook
  programs are root-owned. The account also has passwordless sudo
  (ADR-0017), so a job can still replace either one.
- Moving the hook means copying the three files again from a commit that
  has been checked, and rebuilding the runner images.
- An already cached runner image keeps the old contents until it is
  rebuilt, and an already created VM keeps the image it was created from.
