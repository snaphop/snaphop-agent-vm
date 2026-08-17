# Security Policy

This file has two roles:

1. It defines the private vulnerability-reporting process.
2. It defines hard security boundaries for humans and AI coding agents
   generating or refactoring code in this repository.

The MUST/MUST NOT rules below are binding. Do not weaken, bypass, or remove
them without explicit maintainer approval.

## Reporting A Vulnerability

Please do not open a public issue or discussion for a suspected vulnerability.

Email <wen@wensington.com> with:

- A description of the issue and potential impact.
- Reproduction steps or a proof of concept.
- Affected files, versions, or commits, if known.
- Your name/handle for credit (optional).

Never include real credentials, tokens, customer data, or PII in the report.
Use redacted examples and coordinate a secure transfer if maintainers need
additional evidence.

You should receive an acknowledgement within three business days and an initial
assessment within ten. We will tell you whether we consider the issue in scope,
agree a fix and disclosure timeline with you, and credit you in the release notes
unless you prefer otherwise. Please give us a reasonable window to ship a fix
before public disclosure.

## Threat Model

This tool runs VMs for AI coding agents. That produces one dominant assumption:

**The guest is untrusted.** An agent inside a VM may run arbitrary code — by
instruction, by mistake, or because something it downloaded told it to. The VM
boundary is the security control, and the tool's job is to avoid punching holes
through it.

Trust levels, most to least trusted:

1. **The operator's shell** — supplies arguments, config, and public keys.
   Trusted to intend what it asks, but its input is still validated because a
   mistake here becomes a host-level operation.
2. **`agent-vm`, libvirt, and QEMU on the host** — the control plane, running
   with the operator's privileges. The only components allowed to touch host
   state.
3. **The guest** — untrusted. Everything it returns is untrusted input.
4. **Container registries** — untrusted content, pinned by digest.

Out of the tool's control, and stated plainly rather than papered over: QEMU/KVM
guest-escape vulnerabilities, host kernel bugs, and what the operator chooses to
do with `--network bridge` or an explicit host-path share.

## Hard Security Boundaries

### Guest Isolation

- A guest MUST NOT receive access to any host filesystem path by default. Host
  path sharing (virtiofs or otherwise) MUST be opt-in per VM, explicit on the
  command line, read-only unless the operator asks for write access, and recorded
  in `vm.json`.
- The state directory, the operator's home directory, host SSH private keys, and
  cloud or registry credentials MUST NEVER be exposed to a guest by any mechanism
  — mount, seed, environment, or metadata service.
- The tool MUST NOT disable or relax host confinement of QEMU (SELinux,
  AppArmor, seccomp) or grant a domain host devices, host PCI passthrough, or
  privileged QEMU options to work around a permission error.
- Guests MUST NOT be given a channel to the host beyond the standard virtio
  devices the tool's `virt-install` argument vector asks for (disk, NIC, RNG,
  serial console, guest agent). Adding a device that crosses the boundary —
  whether through our argv or through `--virt-install-arg` defaults — requires an
  ADR and maintainer approval.
- Data returned by a guest — guest agent responses, DHCP lease contents, console
  output, hostnames, command output — MUST be treated as untrusted: validated
  before use, never interpolated into a shell command or a path, and never
  trusted to identify a VM.

### Secrets, Credentials, And Sensitive Data

- MUST NOT commit real credentials, access/refresh tokens, private keys, customer
  data, production data, or PII.
- Only SSH **public** keys are injected into a guest. A private key MUST NEVER be
  written into a base image, a cloud-init seed, a disk image, or this repository.
- MUST NOT bake credentials, tokens, or registry secrets into a base image.
  Base images are shared by every VM built on them and are cached indefinitely.
- Registry credentials, when needed, MUST come from the host's existing container
  auth mechanism and MUST NOT be copied into the state directory or logged.
- MUST NOT log secrets, registry authorization headers, the contents of
  operator-supplied cloud-init user-data, or the contents of any file the operator
  passes in. Log paths, sizes, and digests instead.
- Types that carry secrets MUST redact them from string/debug representations.
- Scripts MUST NOT echo secrets or pass them as command-line arguments where a
  stdin, file, or secret-store mechanism exists — argument vectors are visible to
  every process on the host, and this tool logs the argument vectors of the
  helpers it runs.

### Destructive Operations And Host State

- Every filesystem path the tool writes to or deletes MUST be resolved (symlinks
  included) and verified to be inside the configured state directory before the
  operation. A path escaping it is a fatal error, never a warning.
- The tool MUST NOT stop, undefine, or delete a libvirt domain, network, volume,
  or storage pool that it did not create and does not have a record of in state.
- `destroy` MUST act only on the domain and paths recorded in that VM's
  `vm.json`, and MUST require confirmation unless `--yes` was given.
- `virsh undefine` MUST NOT be invoked with `--remove-all-storage` or any other
  flag that delegates file deletion to libvirt. The tool deletes its own files, after
  the containment check, so that what gets removed is decided in one place.
- A base image MUST NOT be deleted while any overlay depends on it, except behind
  an explicit `--force` that names the consequence.
- Base images MUST be immutable once built, opened read-only when backing a VM,
  and assembled in a temporary location that is renamed into place only on
  success — no VM may ever boot a partially built base image.
- A failed operation MUST roll back what it created. When rollback itself fails,
  the tool MUST report exactly what remains on the host and exit non-zero. It MUST
  NEVER report success while host state is left behind.
- The tool MUST NOT modify host network interfaces, bridges, routing, or firewall
  rules. Missing host prerequisites are reported with the command the operator can
  run.

### Networking Boundaries

- NAT MUST remain the default network mode.
- Bridged networking places an untrusted guest directly on the operator's LAN,
  reachable by every host on it. It MUST require an explicit `--network bridge`
  and MUST NEVER be selected implicitly by fallback, auto-detection, or because
  NAT setup failed.
- The tool MUST NOT create host port forwards, publish guest services on host
  interfaces, or expose a guest beyond the selected network mode.
- The libvirt NAT network the tool defines MUST NOT be given host-wide routing,
  additional host interfaces, or forwarding rules beyond libvirt's standard NAT
  behavior.
- The network mode of every VM MUST be recorded in `vm.json` and visible in
  `agent-vm list`/`info`, so exposure is auditable after the fact.

### Image Provenance And Supply Chain

- Every base image MUST record the source OCI image digest, and that digest MUST
  be verified against what was pulled. A mismatch is fatal.
- MUST NOT fall back to an unpinned tag, an alternate registry, or an insecure
  transport when a pinned pull fails.
- MUST NOT disable TLS verification for registry access, and MUST NOT accept a
  registry reference from a guest or from any untrusted source.
- New top-level dependencies, base images, registries, and external tools require
  explicit approval and security consideration. Because the design delegates to host
  tools, adding a tool expands the trusted computing base — and so does raising a
  version floor, which can silently stop the tool from working on a host.
- Tool versions MUST be recorded in `manifest.json` and `vm.json`, so an artifact
  built by a known-vulnerable tool version can be identified after the fact.
- Dependency and source image versions SHOULD be pinned according to the project's
  update policy.

### Input, Output, And Subprocess Execution

- Treat CLI arguments, config files, environment variables, cloud-init input,
  registry metadata, guest responses, and helper-process output as untrusted.
- Validate type, length, range, format, and — for paths — containment, before use.
  VM names MUST match `^[a-z0-9][a-z0-9-]{0,30}[a-z0-9]$`; sizes and counts MUST be
  parsed and bounded, not passed through as strings.
- Every tool (`virt-install`, `virsh`, `qemu-img`, `podman`/`skopeo`, libguestfs,
  `ip`, `ssh`) MUST be invoked through `internal/hostexec` with an explicit argument
  vector. MUST NOT build command strings for a shell, MUST NOT use `sh -c`, and MUST
  NOT pass untrusted values where a tool would interpret them as options — terminate
  options and validate first.
- Secrets MUST NOT be passed as command-line arguments. Argument vectors are logged
  and are visible to every process on the host.
- Generated cloud-init data and network XML MUST be produced with proper escaping
  for their format. MUST NOT concatenate untrusted values into XML or YAML.
- Tool output MUST be parsed from a machine-readable mode where one exists, and a
  parse failure MUST be an error rather than a silently empty result — "no address
  found" and "output format changed" must not be indistinguishable.
- Errors MUST NOT leak credentials, registry tokens, private key material, or the
  contents of operator-supplied files. Non-sensitive host detail (paths, exit codes,
  libvirt errors, console tails) is intentionally included, because this tool is
  useless without it.

### Build, CI, And Release

- CI and release workflows MUST use least-privilege credentials and MUST NOT expose
  secrets to untrusted pull-request code.
- Integration tests MUST run only against a disposable host, MUST use a dedicated
  state directory and the `agent-vm-test-` name prefix, and MUST NOT touch domains
  or volumes they did not create.
- A mutating live test on a host running VMs that matter requires explicit
  authorization and confirmation that the target is safe.
- Release artifacts MUST be built from a tagged commit through the documented
  workflow; merging MUST NOT publish anything.

### Tests And Review

- MUST NOT disable, skip, or weaken security-relevant tests to make a change pass.
- Changes to guest isolation, host path sharing, network mode selection, SSH key or
  cloud-init handling, subprocess invocation, path containment, the destroy/rollback
  path, image provenance, dependencies, or release workflows SHOULD receive a
  security-focused review under [`CODE_REVIEW.md`](./CODE_REVIEW.md).
- Security regression tests SHOULD assert both the intended success path and the
  prohibited behavior — that a path outside the state directory is refused, that an
  unknown domain is not undefined, that bridged mode is never implicit.

## Supported Versions

| Version | Supported |
|---|---|
| Latest `0.x` release | Yes |
| Older releases | No |

While the project is `0.x`, security fixes land on the latest release only. There
are no backports.

## Scope

In scope: code in this repository, the `virt-install` argument vector,
cloud-init user-data, and NAT network XML it generates, its handling of host
state and secrets, and the way it invokes its dependencies. A defect that lets a
guest reach host state the design says it
cannot, or that causes the tool to destroy host state outside its own state
directory, is in scope and important.

Out of scope: vulnerabilities in libvirt, QEMU/KVM, the Linux kernel, `virt-install`,
libguestfs, `podman`, or container images (report those upstream — though if *our*
invocation of one of them is what creates the exposure, that is in scope);
guest-to-host escapes attributable to the hypervisor rather than to our
configuration; the documented consequences of
opt-in features such as bridged networking or an explicit host-path share; social
engineering; and purely volumetric denial-of-service reports.
