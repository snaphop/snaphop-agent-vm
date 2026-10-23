# 22. Install Java and Rust on the runner image

Date: 2026-10-23

## Status

Accepted

Extends [ADR-0013](./0013-self-hosted-github-actions-runner-variant.md),
[ADR-0016](./0016-install-docker-on-the-runner-image.md), and
[ADR-0017](./0017-run-the-github-actions-runner-as-github-runner.md). The
runner variant stays the slim recipe plus one shared section. That section
also installs a JDK, Rust, and five cargo tools.

Extended by [ADR-0023](./0023-install-go-and-maven-on-the-runner-image.md),
which adds Maven after the JDK and Go with four Go tools after Rust.

## Context

The organization's workflows build Java and Rust projects. On a runner
image a job had to install both toolchains itself, every time, before it
could start the build. The Rust jobs also run `cargo llvm-cov`,
`cargo audit`, `cargo deny`, `cargo fuzz`, and `worker-build`, and building
those from source takes several minutes per job.

The full image already ships both toolchains. It installs the current
Eclipse Temurin LTS through mise, and Rust through rustup into a shared
`/usr/local/rustup` with `CARGO_HOME=/usr/local/cargo`. `agent-vm update`
runs `rustup update` with those two paths as root.

A runner image has no mise, and jobs run as `github-runner`, not as the SSH
account. Workflow actions such as `dtolnay/rust-toolchain` run
`rustup toolchain install` as the job account. On a root-owned
`RUSTUP_HOME` that step fails with "Permission denied", even when the
toolchain it asks for is already installed, because rustup writes its
download and lock files there.

A job does not get `/etc/environment`. The runner service passes the lines
in `/opt/actions-runner/.env` to each job. `config.sh` keeps lines already
in `.env` and adds `JAVA_HOME` only when it is missing.

## Decision

- Two scripts join the shared runner section, after `runner-git.sh` and
  before Docker: `templates/distro/runner-java.sh` and
  `templates/distro/runner-rust.sh`. They are the same on Ubuntu, Fedora,
  and Arch. The runner install before them has already added the C
  toolchain, the OpenSSL headers, and `pkg-config` that the cargo tools
  compile against.
- **Java.** The newest Temurin LTS JDK. The script asks Adoptium's API,
  which mise also reads, for `most_recent_lts`, then for that major's
  newest Linux JDK build for the host architecture (`x64` or `aarch64`).
  It checks the archive against the SHA-256 the API publishes before it
  extracts to `/usr/lib/jvm/temurin-<major>`. The JDK's commands are
  linked into `/usr/local/bin`. `JAVA_HOME` is written to
  `/etc/environment` and to the runner's `.env`. The build fails if
  `java -version` does not name Temurin and an LTS, or if `javac` cannot
  run. mise is not installed on the runner image for this.
- **Rust.** rustup into the same `/usr/local/rustup` and
  `/usr/local/cargo` the full image uses, so `agent-vm update` moves it
  with the step it already has. Stable is the default toolchain, with the
  `default` profile (`rustfmt`, `clippy`), the `llvm-tools` component that
  `cargo llvm-cov` needs, and the `wasm32-unknown-unknown` target that
  `worker-build` compiles to. Nightly is installed beside it with the
  `minimal` profile, because `cargo fuzz` runs only on nightly. The rustup
  proxies are linked into `/usr/local/bin`. `RUSTUP_HOME` is written to
  `/etc/environment` and to the runner's `.env`. `CARGO_HOME` is left
  unset, so `cargo install` writes into each account's own `~/.cargo`.
- **`RUSTUP_HOME` is owned by `github-runner`.** A job can add a toolchain,
  component, or target without sudo. The `agent` account can read it and
  run the toolchains. `agent-vm update` gains one step after `rustup
  update`: `chown -R github-runner:github-runner -- /usr/local/rustup`, as
  root, run only when the guest has `agent-vm-github-runner`. Without it,
  what root's update replaced would be owned by root again.
- **The cargo tools are not pinned.** `cargo install --locked` from
  crates.io, at each crate's newest version, into `/usr/local/cargo/bin`,
  linked into `/usr/local/bin`: `cargo-llvm-cov`, `cargo-audit`,
  `cargo-deny`, `cargo-fuzz`, and `worker-build`. The cargo registry and the
  build directory are scratch and are removed. `agent-vm update` does not
  move them; a rebuild picks up new releases.
- **Checked as the job account.** Before the image is committed, the
  script runs `rustc`, `cargo`, `rustup`, `cargo fmt`, `cargo clippy`,
  `rustc +nightly`, and each cargo tool as `github-runner`, with
  `HOME=/opt/actions-runner` and only the `RUSTUP_HOME` a job gets. It also
  checks that `cargo install --list` reports each tool.
- No new host tool, no manifest change, and no change to `configure`. The
  `.env` lines are written during the image build, and `configure` keeps
  lines it does not own.

## Consequences

Easier:

- A Java or Rust workflow starts building as soon as it checks out. The
  five cargo tools are already on `PATH`.
- Workflow steps that run `rustup` as the job account work as they do on a
  GitHub-hosted runner.
- The full image and the runner image share Rust's paths and its update
  step.

Harder:

- The runner image is larger by a JDK, two Rust toolchains, and the five
  tools, and its build is longer by the time it takes to compile them.
- The JDK, the Rust toolchains, and the cargo tools are not pinned. A
  rebuild picks up a new LTS major, new Rust releases, and new tool
  releases, which can change a tool's behaviour under a workflow. The manifest records only the source
  image's digest, so a rebuild is not reproducible (ADR-0006).
- `agent-vm update` moves Rust but not the JDK. A new Temurin build reaches
  a runner VM when its image is rebuilt and the VM is created again.
- A job can change the shared toolchains, and the `agent` account sees
  that change. The guest is already untrusted, and jobs already have sudo
  (ADR-0017).
- An image already cached gains none of this until it is rebuilt with
  `agent-vm image build <family>-runner --force`.
