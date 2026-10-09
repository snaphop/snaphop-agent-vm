# Security Policy

This file defines private vulnerability reporting and the binding security
boundaries for SnapHop Agent VM. [`docs/cli.md`](./docs/cli.md) is the public
command contract, [`docs/architecture.md`](./docs/architecture.md) is the system
map, and the ADRs under [`docs/decisions/`](./docs/decisions/) record the
decisions behind both. This policy states the rules that must stay visible
during implementation, review, release, and operation. The MUST/MUST NOT rules
cannot be weakened without explicit maintainer approval and, where the decision
is architectural, an ADR.

Where this policy and `docs/architecture.md` describe the same security
boundary, this policy is binding.

## Reporting A Vulnerability

**Never open a public issue, pull request, or discussion for a suspected
vulnerability.** This rule is about visibility and applies wherever the report
would be readable by people who do not already have access to the code.

This repository is public. A GitHub issue, pull request, or discussion is not a
private channel. Report only by email to <security@snaphop.com>.

Include:

- A description of the issue and its potential impact.
- Reproduction steps or a proof of concept, with sensitive values redacted.
- Affected files, versions, or commits, if known.
- Your name or handle for credit (optional).

Never include a private key, GitHub token, registry credential, cloud
credential, authorization header, password, the contents of operator-supplied
cloud-init or any other operator-supplied file, guest filesystem contents,
customer or production data, or a production log that contains any of those.
Use synthetic examples and coordinate a secure transfer if maintainers need
additional evidence.

You should receive an acknowledgement within three business days and an initial
assessment within ten. We will tell you whether we consider the issue in scope,
agree a fix and disclosure timeline with you, and credit you in the release
notes unless you prefer otherwise. Please give us a reasonable window to ship a
fix before public disclosure.

## Threat Model

This tool creates short-lived QEMU/KVM virtual machines for AI coding agents.
The guest is the sandbox. The tool's job is to avoid punching holes through
that boundary, and to avoid destroying host state it does not own.

- **The guest is untrusted.** An agent inside a VM may run arbitrary code, by
  instruction, by mistake, or because something it downloaded told it to.
  Passwordless `sudo` and permissive agent configuration inside the guest are
  deliberate, and they are sound only while the VM itself remains the sandbox.
- **Every input boundary is untrusted.** CLI arguments, config files,
  environment variables, cloud-init input, registry metadata, guest-agent
  responses, DHCP leases, console output, and the stdout of every helper
  process are hostile until validated.
- **Failure is not success.** A path outside the state directory, an unknown
  libvirt domain, a digest mismatch, an implicit bridged network, or a cleanup
  that leaves host state behind is a failure. The tool MUST NOT report success
  over any of them.
- **NAT is the default exposure.** Bridged networking places the guest on the
  operator's LAN. It is an explicit trust-boundary change, never a fallback.
- **A remote hypervisor is a broader grant than libvirt.** `qemu+ssh://` lets
  this tool run host tools as the named account on that machine
  ([ADR-0010](./docs/decisions/0010-drive-a-remote-hypervisor-by-running-host-tools-over-ssh.md)).
  The URI must say so. Other remote transports are refused because they reach
  libvirt and give no shell the tool can build disks with.
- **Provenance is part of isolation.** Base images are immutable, their source
  digest is recorded and checked, and a VM disk is a copy-on-write overlay. An
  unpinned fallback is a different artifact than the one the operator asked for.
- **QEMU, KVM, and the host kernel are outside this tool's control.** So is the
  operator's decision to use `--network bridge`. Those limits are stated here
  rather than papered over. Host path sharing is not implemented; the rules
  below govern it in advance so that adding it cannot happen by accident.

Trust, from most to least:

1. **The operator's shell** — supplies arguments, config, and SSH public keys.
   Trusted to intend what it asks, but its input is still validated because a
   mistake here becomes a host-level operation.
2. **`agent-vm`, libvirt, and QEMU on the host** — the control plane, running
   with the operator's privileges. The only components allowed to touch host
   state. With a `qemu+ssh://` URI this spans two machines: the ssh connection
   is part of the control plane, terminates on the hypervisor, and is never
   reachable from a guest.
3. **The guest** — untrusted. Everything it returns is untrusted input.
4. **Container registries** — untrusted content, pinned by digest.

The same boundaries are drawn in
[`docs/architecture.md`](./docs/architecture.md).

## Sensitive Data Classes

- **Host credentials:** SSH private keys, GitHub tokens and `gh` credentials,
  registry authorization, cloud credentials, and any credential the
  `qemu+ssh://` account can use.
- **Operator-supplied content:** cloud-init user-data and any other file the
  operator passes in. These may contain secrets even when this tool does not
  need them.
- **Guest content:** the guest filesystem, guest-generated private keys, and
  whatever the guest prints on its console.
- **Remote-hypervisor material:** the ssh destination, key paths, and anything
  that would authenticate the control-plane connection. A password does not
  belong in a libvirt URI.

These values MUST NOT enter source control, normal logs, argument vectors,
base-image layers, cloud-init seeds (except an SSH **public** key), metric
labels, or routine issue reports. A Tailscale auth key is a credential of
that kind. The one path that carries it into a guest is the stdin of
`tailscale up` after the guest has booted, and only when the operator names
a file with `create --tailscale-auth-key-file` (ADR-0014). That path does
not put the key in a base image, a cloud-init seed, `vm.json`, a log, or an
argument vector. This tool has no metrics or traces. Guest
content belongs to that VM and is destroyed with it; it MUST NOT be copied into
a base image, the repository, or a log. Synthetic development material must be
clearly non-production and must not contain real credentials or customer data.

## Fail-Closed Outcomes

These outcomes are never success. The binding rule for each lives in the
section named here.

- A path that resolves outside the state directory is fatal
  ([Destructive Operations And Host State](#destructive-operations-and-host-state)).
- A libvirt domain, network, volume, or storage pool this tool did not record
  is never stopped, undefined, or deleted (same section). File deletion is not
  delegated to libvirt.
- A source-image digest mismatch is fatal, with no unpinned or insecure
  fallback ([Image Provenance And Supply Chain](#image-provenance-and-supply-chain)).
- Bridged networking is never selected by fallback, detection, or omission
  ([Networking Boundaries](#networking-boundaries)).
- A parse failure is an error. "No address found" and "the tool's output format
  changed" stay distinguishable
  ([Input, Output, And Subprocess Execution](#input-output-and-subprocess-execution)).
- Rollback that cannot finish reports exactly what remains and exits non-zero.
  Success is never reported while host state was left behind
  ([Destructive Operations And Host State](#destructive-operations-and-host-state)).

## Guest Isolation

- A guest MUST NOT receive access to any host filesystem path by default. Host
  path sharing (virtiofs or otherwise) MUST be opt-in per VM, explicit on the
  command line, read-only unless the operator asks for write access, and
  recorded in `vm.json`.
- The state directory, the operator's home directory, host SSH private keys,
  and cloud or registry credentials MUST NEVER be exposed to a guest by any
  mechanism — mount, seed, environment, or metadata service.
- The tool MUST NOT disable or relax host confinement of QEMU (SELinux,
  AppArmor, seccomp) or grant a domain host devices, host PCI passthrough, or
  privileged QEMU options to work around a permission error.
- Guests MUST NOT be given a channel to the host beyond the standard virtio
  devices the tool's `virt-install` argument vector asks for (disk, NIC, RNG,
  serial console, guest agent). Adding a device that crosses the boundary —
  whether through our argv or through `--virt-install-arg` defaults — requires
  an ADR and maintainer approval.
- Data returned by a guest — guest agent responses, DHCP lease contents,
  console output, hostnames, command output — MUST be treated as untrusted:
  validated before use, never interpolated into a shell command or a path, and
  never trusted to identify a VM. A guest address used as an ssh destination
  MUST be a bare IP address.
- The coding agents inside a guest are deliberately configured in their most
  permissive modes, and passwordless `sudo` for the login user is deliberate
  too. A change that weakens guest isolation, the default network mode, or the
  credential boundary is a change to what those permissions mean, not an
  isolated convenience.

## Secrets, Credentials, And Sensitive Data

- MUST NOT commit real credentials, access or refresh tokens, private keys,
  customer data, production data, or PII.
- SSH keys injected into a guest are **public** keys only. A private key MUST NEVER
  be written into a base image, a cloud-init seed, a disk image, the state
  directory, or this repository. The tool may locate a private key that sits
  beside a recorded public key and pass that path to `ssh`. It MUST NOT read
  the key, copy it, or log it.
- A Tailscale auth key may cross into a guest in exactly one way: the operator
  names a file with `create --tailscale-auth-key-file`, and after SSH is up
  the tool passes that file's contents on the stdin of the guest's `up`
  command. The key MUST NOT be a flag value, an environment variable, an
  argument vector, a base-image layer, generated cloud-init user-data, or a
  field in `vm.json`. The guest script MUST remove its copy when `tailscale
  up` returns, including on failure. The guest can read the key while that
  command runs, so the key SHOULD be one-time and ephemeral. Do not put the
  key in cloud-init, the base image, or an argument to avoid that window.
- A guest generating a key pair for its own accounts at first boot is a
  different thing and is allowed: that key is created inside the VM, never
  leaves it except as the public half described below, and is destroyed with
  the VM.
- MUST NOT bake credentials, tokens, or registry secrets into a base image.
  Base images are shared by every VM built on them and are cached indefinitely.
  The coding agents a base image carries therefore ship configured but
  **unauthenticated**. A login or API key reaches a VM separately, through
  `--cloud-init` or from inside the guest.
- Forge and cloud credentials MUST stay on the host. `--github-ssh-key` runs
  `gh` on the client with the operator's existing login and sends only the
  **public** half of a key the guest generated for itself. `gh` MUST NOT be
  authenticated inside a guest by this tool. The operator's `gh` credential
  MUST NEVER enter a guest, an image, a seed, `vm.json`, a log, or an argument
  vector.
- A `-runner` base image carries the GitHub Actions self-hosted runner and the
  command that registers it. The image, the cloud-init seed, generated
  user-data, and `vm.json` contain no registration token. A registration
  token may cross into a guest in exactly one way: the organization is set
  with `create --github-org` (or `[github] org`, or `AGENT_VM_GITHUB_ORG`),
  the image is a `-runner` variant, and after SSH is up the tool fetches the
  token on the client with
  `gh api --method POST orgs/<org>/actions/runners/registration-token --jq .token`
  and passes that value on the stdin of
  `sudo -n /usr/local/sbin/agent-vm-github-runner configure --token-file -`
  (ADR-0015). The token MUST NOT be a flag value, an environment variable, an
  argument vector, a base-image layer, generated cloud-init user-data, a field
  in `vm.json`, or a log line. The type that carries it MUST redact it. The
  guest can read the token while `config.sh` runs, so the value that crosses
  is the short-lived registration token. A create that names no organization
  does not fetch a token; the operator registers that guest by running
  `agent-vm-github-runner configure` inside it. `config.sh` stores the runner
  credential on the guest. Jobs run as the `github-runner` account. That
  account's shell is `nologin`, so it is not an SSH login. It has
  passwordless sudo (`github-runner ALL=(ALL) NOPASSWD:ALL`), so a job can
  act as root inside the guest (ADR-0017). It is a member of the `docker`
  group so a job can use the daemon (ADR-0016). A job that can open the
  Docker socket can start a privileged container and act as root inside the
  guest. Destroying a VM
  whose record names a runner deletes that runner at GitHub before the domain
  is undefined. A runner registered by hand, which has no record, stays at
  GitHub; unregister it with `agent-vm-github-runner remove` or in the GitHub
  UI.
- Registry credentials, when needed, MUST come from the host's existing
  container auth mechanism and MUST NOT be copied into the state directory or
  logged.
- A `qemu+ssh://` connection authenticates with the operator's existing ssh
  keys. A password in the libvirt URI is refused. The private key stays in the
  operator's ssh configuration.
- MUST NOT log secrets, registry authorization headers, the contents of
  operator-supplied cloud-init user-data, or the contents of any file the
  operator passes in. Log paths, sizes, and digests instead.
- Types that carry secrets MUST redact them from string and debug
  representations.
- Scripts MUST NOT echo secrets or pass them as command-line arguments where a
  stdin, file, or secret-store mechanism exists. Argument vectors are visible
  to every process on the host, and this tool logs the argument vectors of the
  helpers it runs.

## Destructive Operations And Host State

- Every filesystem path the tool writes to or deletes MUST be resolved
  (symlinks included) and verified to be inside the configured state directory
  before the operation, except the host preparation `agent-vm setup` performs
  when the operator asks for it (ADR-0018). That command installs packages,
  enables the libvirt service, adds the operator to groups, and may grant the
  QEMU account search permission, and only search permission, on a directory
  above the state directory. It MUST NOT grant read or write. A path escaping
  the state directory from any other command is a fatal error, never a warning.
- The tool MUST NOT stop, undefine, or delete a libvirt domain, network,
  volume, or storage pool that it did not create and does not have a record of
  in state.
- `destroy` MUST act only on the domain and paths recorded in that VM's
  `vm.json`, and MUST require confirmation unless `--yes` was given.
- `virsh undefine` MUST NOT be invoked with `--remove-all-storage` or any other
  flag that delegates file deletion to libvirt. The tool deletes its own files,
  after the containment check, so that what gets removed is decided in one
  place.
- A base image MUST NOT be deleted while any overlay depends on it, except
  behind an explicit `--force` that names the consequence.
- Base images MUST be immutable once built, opened read-only when backing a VM,
  and assembled in a temporary location that is renamed into place only on
  success. No VM may ever boot a partially built base image.
- A failed operation MUST roll back what it created. When rollback itself
  fails, the tool MUST report exactly what remains on the host and exit
  non-zero. It MUST NEVER report success while host state is left behind.
- The tool MUST NOT modify host network interfaces, bridges, routing, or
  firewall rules. Missing host prerequisites are reported with the command the
  operator can run.

## Networking Boundaries

- NAT MUST remain the default network mode.
- Bridged networking places an untrusted guest directly on the operator's LAN,
  reachable by every host on it. It MUST require an explicit `--network bridge`
  and MUST NEVER be selected implicitly by fallback, auto-detection, or because
  NAT setup failed.
- The tool MUST NOT create host port forwards, publish guest services on host
  interfaces, or expose a guest beyond the selected network mode.
- Joining a Tailscale network is not a libvirt network mode and MUST NOT
  change the host's routes, interfaces, or firewall. It MUST be requested
  explicitly on `create` with `--tailscale-auth-key-file`, and MUST NOT be
  selected by a config key or an environment variable. `list` and `info` MUST
  show that the guest joined, because the tailnet is another way to reach it.
  `destroy` does not remove the tailnet node.
- The libvirt NAT network the tool defines MUST NOT be given host-wide routing,
  additional host interfaces, or forwarding rules beyond libvirt's standard NAT
  behavior.
- The network mode of every VM MUST be recorded in `vm.json` and visible in
  `agent-vm list` and `agent-vm info`, so exposure is auditable after the fact.
- ssh into a guest sets `StrictHostKeyChecking=no` and
  `UserKnownHostsFile=/dev/null`. A VM generates a new host key every time it
  is created, and collecting a conflicting entry per address would train the
  operator to clear them. That tradeoff is bounded on the default NAT network,
  which is host-local. On a bridge the guest shares the operator's LAN, and
  the missing host-key check is a weaker guarantee. The guest address is still
  validated as a bare IP before it is placed in the ssh argument vector.
- Because the host key is not checked, the ssh destination MUST NOT be chosen
  by the guest. On NAT it comes from libvirt's DHCP leases, never from the
  guest agent. On a bridge, where the guest agent is the only source, only the
  interface whose MAC is recorded in `vm.json` is used. A guest that lies about
  that interface's address can still misdirect the connection on a bridge.
- The ssh connection to a guest runs on the client and uses the operator's
  keys. It is not a channel from the guest back to the host.

## Image Provenance And Supply Chain

- Every base image MUST record the source OCI image digest, and that digest
  MUST be verified against what was pulled. A mismatch is fatal.
- MUST NOT fall back to an unpinned tag, an alternate registry, or an insecure
  transport when a pinned pull fails.
- MUST NOT disable TLS verification for registry access, and MUST NOT accept a
  registry reference from a guest or from any untrusted source.
- New top-level dependencies, base images, registries, and external tools
  require explicit approval and security consideration. Because the design
  delegates to host tools, adding a tool expands the trusted computing base —
  and so does raising a version floor, which can silently stop the tool from
  working on a host.
- Tool versions MUST be recorded in `manifest.json` and `vm.json`, so an
  artifact built by a known-vulnerable tool version can be identified after the
  fact.
- Dependency and source image versions SHOULD be pinned according to the
  project's update policy. Go modules and GitHub Actions are updated through
  the repository's Dependabot configuration. A new dependency or a raised
  minimum host-tool version still needs the approval `AGENTS.md` requires.

## Input, Output, And Subprocess Execution

- Treat CLI arguments, config files, environment variables, cloud-init input,
  registry metadata, guest responses, and helper-process output as untrusted.
- Validate type, length, range, format, and — for paths — containment, before
  use. VM names MUST match `^[a-z0-9][a-z0-9-]{0,30}[a-z0-9]$`. Sizes and
  counts MUST be parsed and bounded, not passed through as strings. vCPU count,
  memory, and disk size each have a finite floor and ceiling.
- Every tool (`virt-install`, `virsh`, `qemu-img`, `podman`, libguestfs, `ip`,
  `ssh`) MUST be invoked through `internal/hostexec` with an explicit argument
  vector. MUST NOT build command strings for a shell, MUST NOT use `sh -c`, and
  MUST NOT pass untrusted values where a tool would interpret them as options.
  Terminate options and validate first.
- **One exception, and only one:** the ssh transport that runs host tools on a
  remote hypervisor. ssh has no mechanism for passing an argument vector
  through untouched — the far side is always a shell — so `internal/hostexec`
  renders the vector to text there. That rendering MUST live in that single
  function, MUST quote every argument such that the remote shell interprets
  nothing in it, and MUST be covered by tests, including against a real shell.
  No other code in this project may build a command string, and this exception
  MUST NOT be widened to compose remote shell logic: each remote operation is
  one argument vector, not a script. Adding a second such place requires an ADR.
- Secrets MUST NOT be passed as command-line arguments. Argument vectors are
  logged and are visible to every process on the host.
- Generated cloud-init data and network XML MUST be produced with proper
  escaping for their format. MUST NOT concatenate untrusted values into XML or
  YAML.
- Tool output MUST be parsed from a machine-readable mode where one exists, and
  a parse failure MUST be an error rather than a silently empty result.
- Errors MUST NOT leak credentials, registry tokens, private key material, or
  the contents of operator-supplied files. Non-sensitive host detail (paths,
  exit codes, libvirt errors, bounded stderr, console tails) is intentionally
  included, because this tool is useless without it. A failed tool's stderr
  excerpt is bounded.

## Logging

- Logs are structured (`log/slog`) on stderr. Helper invocations are logged
  at debug level, with the argument vector, duration, and exit status, and
  `--verbose` is what shows that log. A failure also names the tool, its
  argument vector, and its exit status in the error the operator sees. That
  is safe only because secrets are never placed in an argument vector.
- Safe fields include the tool name, the validated argument vector, exit
  status, duration, paths, sizes, digests, the VM name, and the network mode.
- MUST NOT log or otherwise retain the classes in **Sensitive Data Classes**.
  In particular, MUST NOT log cloud-init contents, operator-supplied file
  contents, authorization headers, private keys, GitHub tokens, or Tailscale
  auth keys.
- The guest serial console is captured to `console.log` inside that VM's state
  directory and removed when the VM is destroyed. It is a guest diagnostic, not
  a host-secret store. Host credentials MUST NOT be written there.
- There are no metrics, traces, or alerts. The process exit code is the status
  signal.

## Resource Limits And Lifecycle

- Header-style admission limits do not apply: this is a foreground CLI, not a
  request gateway. The limits that do apply MUST stay finite: VM name length,
  vCPU count, memory, maximum memory, disk size, host-tool timeouts, and the
  stderr excerpt carried in an error.
- A command that can block MUST carry a deadline. The default host-tool timeout
  is finite; a long operation such as an image build sets its own. A timeout is
  reported as a timeout, naming the tool.
- Per-VM and per-image locks MUST cover concurrent `create`, `destroy`, and
  image builds. Libvirt remains the source of truth for whether a domain is
  running.
- On a failed `create`, roll back the domain and the per-VM directory. A
  boot-wait, Tailscale-join, or GitHub-registration failure may retain the
  recorded VM for inspection; that retention MUST be visible in the error,
  not reported as a clean create. Built base images and the shared NAT
  network stay in place because other VMs reuse them.
- Integration tests MUST run only against a disposable host, MUST use a
  dedicated state directory and the `agent-vm-test-` name prefix, and MUST NOT
  touch domains or volumes they did not create. A mutating live test on a host
  running VMs that matter requires explicit authorization.

## Configuration, Build, CI, And Release

- Secrets MUST come from the operator's existing ssh agent, `gh` login, or
  container auth. They MUST NOT be committed, placed in `.env.example`, baked
  into an image layer, passed as command-line arguments, or printed by CI.
- `.env.example` documents environment variables only. The binary reads the
  process environment and the config file, not that example. Never put a secret
  in either file.
- Startup and `agent-vm doctor` diagnostics MUST NOT print secrets. They may
  name a missing tool, its version, and the command that would fix the host.
- New top-level dependencies, base images, registries, external tools, and
  deployment mechanisms require explicit approval and security review, and an
  ADR where `AGENTS.md` requires one.
- CI runs on GitHub-hosted runners. The pull-request and `master` workflow has
  `contents: read` and MUST NOT receive secrets that untrusted pull-request
  code could read. In the release workflow only the publish job has
  `contents: write`, so it can attach a release; it runs no repository code,
  and the build job that does runs with `contents: read`. Third-party actions
  are pinned to a commit SHA. This repository does not use a persistent
  self-hosted runner.
- Release artifacts MUST be built from a tagged commit through
  `.github/workflows/release.yml`. Merging MUST NOT publish anything. Pushing a
  `vX.Y.Z` tag builds the static binaries and attaches them, with `LICENSE` and
  `NOTICE`, to a GitHub Release.
- A known vulnerability in a host tool this program invokes is handled by
  raising that tool's floor, or by changing how the tool is invoked, with the
  approval a new floor requires. This repository does not scan or ship a
  service image.

## Operator Responsibilities

- Treat the guest as untrusted. Do not put a host credential, a private key, or
  production data into cloud-init or the guest unless that disclosure is
  intentional and the VM is still an acceptable place for it.
- Scan or otherwise decide the trust of anything the guest will run. This tool
  isolates the guest from the host. It does not decide that a file inside the
  guest is safe.
- Use bridged networking only when the guest is meant to be directly reachable
  on that LAN. NAT blocks unsolicited LAN connections to the guest, but permits
  guest-initiated access to the host, LAN, and other guests. Apply the
  [host firewall policy](./docs/host-setup.md#host-firewalls-and-the-virbrn-bridge)
  your workloads require before running untrusted guests; NAT alone does not
  satisfy host-service or LAN isolation requirements.
- Prefer a one-time, ephemeral Tailscale auth key, tagged when the tailnet
  uses tags. The guest can read the key while `tailscale up` runs. A node
  that is not ephemeral stays on the tailnet after `destroy` until it is
  removed in the tailnet's admin console. Joining a tailnet puts the guest
  on that network; it is a separate choice from NAT or bridge.
- A `qemu+ssh://` URI grants command execution as that account on that host.
  Use it only against a hypervisor the operator is willing to administer.
- Destroy the VM when the task is done. The overlay, the seed, and
  `console.log` are removed with it. Do not treat a base image as a private
  per-VM store; every VM built from it shares it.
- Do not point the integration suite at a state directory or a libvirt host
  that holds VMs someone cares about.

## Tests And Review

- MUST NOT disable, skip, or weaken security-relevant tests to make a change
  pass.
- Security regression tests SHOULD assert both the intended success path and
  the prohibited behavior: a path outside the state directory is refused, an
  unknown domain is not undefined, bridged mode is never implicit, a digest
  mismatch is fatal, and a secret marker does not appear in a log, an error, an
  argument vector, `vm.json`, a base image, or a seed.
- Test the remote-shell exception against a real shell: every argument is
  quoted, and the exception is not a place to compose a script.
- Test that guest-influenced values (addresses, leases, console text) are
  validated before they reach a path, a command, or an identity decision.
- Changes to guest isolation, host path sharing, network mode selection, SSH
  key or cloud-init handling, subprocess invocation, path containment, the
  destroy and rollback path, image provenance, dependencies, or release
  workflows SHOULD receive a security-focused review under
  [`CODE_REVIEW.md`](./CODE_REVIEW.md).
- A significant change to guest isolation, boot, the image cache, networking,
  where host tools run, or a reimplementation of a host tool requires an ADR
  and an `[Unreleased]` changelog entry.

## Supported Versions

| Version | Supported |
|---|---|
| Latest `0.x` release | Yes |
| Older releases | No |

While the project is `0.x`, security fixes land on the latest release and on
`master`. There are no backports. Fixes ship as a new tagged release. Merging
does not publish a release. Report the version (`agent-vm --version`) or the
commit, and the host tool versions `agent-vm doctor` prints when they matter.

## Scope

In scope: code in this repository, the `virt-install` argument vector,
cloud-init user-data, and NAT network XML it generates, its handling of host
state and secrets, and the way it invokes its dependencies. A defect that lets
a guest reach host state the design says it cannot, or that causes the tool to
destroy host state outside its own state directory, is in scope and important.
An integration flaw in this tool's use of libvirt, QEMU, libguestfs, `podman`,
or `ssh` is in scope even when the underlying project is not.

Out of scope: vulnerabilities in libvirt, QEMU/KVM, the Linux kernel,
`virt-install`, libguestfs, `podman`, or container images (report those
upstream — though if our invocation of one of them is what creates the
exposure, that is in scope); guest-to-host escapes attributable to the
hypervisor rather than to our configuration; the documented consequences of
opt-in features such as bridged networking and joining a Tailscale network;
social engineering; and purely
volumetric denial-of-service reports.
