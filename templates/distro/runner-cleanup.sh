#!/bin/sh
# Install the runner's job-started disk hook.
#
# Runs as root during the image build, after the runner account and Docker
# exist. The three programs are the copies of snaphop-build-deploy
# .github/actions/runner-disk from commit
# c68e4b2821de81f4d599fdbb48b162097e90b046, the commit the snaphop-ai-stack
# installer pins. The image build does not clone that repository.
#
# GitHub's runner reads ACTIONS_RUNNER_HOOK_JOB_STARTED from the runner's
# .env when the service starts. config.sh keeps an existing .env and may
# append to it. The programs live outside the runner directory, owned by
# root, so a job cannot replace them by writing under that directory.
# configure puts the two .env lines back when they are missing.
set -eu

prefix=${AGENT_VM_RUNNER_PREFIX:-/opt/actions-runner}
dest=${AGENT_VM_RUNNER_HOOK_DIR:-/usr/local/libexec/snaphop-runner}
src_dir=${1:-/tmp}

headroom=$src_dir/agent-vm-runner-headroom.py
job_py=$src_dir/agent-vm-runner-job-started.py
job_sh=$src_dir/agent-vm-runner-job-started.sh

for f in "$headroom" "$job_py" "$job_sh"; do
    if [ ! -f "$f" ]; then
        echo "runner-cleanup: missing $f" >&2
        exit 1
    fi
done

# A hook inside the runner directory would be whatever the last job wrote.
case "$dest" in
    "$prefix" | "$prefix"/*)
        echo "runner-cleanup: the hook must live outside $prefix" >&2
        exit 1
        ;;
esac

if ! id github-runner >/dev/null 2>&1; then
    echo "runner-cleanup: the github-runner account is missing; install the runner first" >&2
    exit 1
fi
if [ ! -d "$prefix" ]; then
    echo "runner-cleanup: $prefix is missing; install the runner first" >&2
    exit 1
fi
if ! command -v python3 >/dev/null 2>&1; then
    echo "runner-cleanup: python3 is not on PATH; the hook needs it" >&2
    exit 1
fi
if ! command -v docker >/dev/null 2>&1; then
    echo "runner-cleanup: docker is not on PATH; install Docker first" >&2
    exit 1
fi

install -d -m 0755 "$dest"
install -m 0644 "$headroom" "$dest/headroom.py"
install -m 0644 "$job_py" "$dest/job-started.py"
install -m 0755 "$job_sh" "$dest/job-started.sh"
chown root:root "$dest" "$dest/headroom.py" "$dest/job-started.py" "$dest/job-started.sh"
rm -rf "$dest/__pycache__"

# Syntax only. py_compile would leave a bytecode cache beside the scripts.
python3 -c 'import ast, sys
for path in sys.argv[1:]:
    ast.parse(open(path, encoding="utf-8").read(), filename=path)
' "$dest/headroom.py" "$dest/job-started.py"

# Prove the hook can start. It never fails a job. Docker is not running
# during the image build, so the probe uses a docker command that exits
# and a threshold that does not ask it to delete anything.
probe=$(mktemp -d)
probe_bin=$probe/bin
mkdir -p "$probe_bin" "$probe/root/_work" "$probe/root/_diag"
cat > "$probe_bin/docker" << 'EOF'
#!/bin/sh
exit 1
EOF
chmod 0755 "$probe_bin/docker"
if ! env -i PATH="$probe_bin:/usr/bin:/bin" HOME=/root \
    SNAPHOP_RUNNER_ROOT="$probe/root" RUNNER_NAME=image-build \
    RUNNER_DISK_PRUNE_BELOW_GIB=0 \
    python3 "$dest/job-started.py" >/dev/null 2>&1; then
    echo "runner-cleanup: the hook did not exit 0" >&2
    rm -rf "$probe"
    exit 1
fi
rm -rf "$probe"

env_file=$prefix/.env
tmp=$(mktemp)
if [ -f "$env_file" ]; then
    grep -vE '^(ACTIONS_RUNNER_HOOK_JOB_STARTED|SNAPHOP_RUNNER_ROOT)=' "$env_file" > "$tmp" || true
fi
printf 'ACTIONS_RUNNER_HOOK_JOB_STARTED=%s\nSNAPHOP_RUNNER_ROOT=%s\n' "$dest/job-started.sh" "$prefix" >> "$tmp"
cat "$tmp" > "$env_file"
rm -f "$tmp"
chown github-runner:github-runner "$env_file"
chmod 0644 "$env_file"
