---
name: review-enhancements
description: Review and classify the repository's complete open enhancement queue without implementing requests. Use when asked to triage, validate, or clarify every enhancement, feature request, or proposal; do not use for one ordinary feature discussion unless explicitly invoked.
---

# Review Enhancements

Review every open enhancement with evidence while preserving the product
contract and repository boundaries.

## Establish Scope And Authority

1. Read [`AGENTS.md`](../../../AGENTS.md), `SECURITY.md`, `CONTRIBUTING.md`,
   `CODE_REVIEW.md`, and the architecture, CLI, host-setup, and ADR
   documentation relevant to the requests.
2. Identify the issue host and repository from Git metadata. Inventory every
   open Feature issue type and every conventional enhancement, feature, or
   proposal label using the repository's live taxonomy; do not infer the queue
   from titles. The conventional label is `enhancement`.
3. Record the initial set and recheck it before finishing. Route suspected
   vulnerabilities through the private process in `SECURITY.md`.
4. Reviewing does not authorize comments, labels, closures, branches, commits,
   implementation, releases, or changes to a host or its libvirt. Establish
   mutation authority before acting externally.

## Understand And Classify Every Request

Read the full issue, discussion, linked work, acceptance criteria, and related
requests. Restate the user need separately from the proposed implementation.
Inspect current code, tests, documentation, released state, and relevant
history to determine whether the capability exists or has already been accepted
or rejected.

Identify the owning boundary. This tool owns short-lived libvirt VMs for
untrusted agents: the OCI base-image cache, copy-on-write overlays, direct
kernel boot, cloud-init seed content, NAT by default with explicit bridged
mode, on-disk state, the `agent-vm` command and its configuration, and
host-side registration of a guest SSH public key. Agent supervisors own which
task receives a VM, what runs inside it, and the SSH public keys or extra
cloud-init they supply. Operators own the host kernel, KVM, libvirt, QEMU,
bridges, firewall, registry and GitHub credentials, and whether a host is
disposable.

A daemon or hosted service, host path sharing, host port forwards, implicit
bridged networking, remote transports other than `qemu+ssh://`, a new distro
family, a change to where guest tooling comes from, and a reimplementation of
`virt-install`, `virsh`, `podman`, `qemu-img`, or libguestfs are outside the
current contract unless an ADR is accepted. Hypervisor and kernel escapes, and
the documented effect of `--network bridge`, are outside this tool's control.

Classify each request:

- **Valid:** a real unmet need falls within this tool's ownership and can be
  pursued without violating a binding policy.
- **Invalid:** the capability exists in an applicable release, duplicates a
  canonical request, belongs to a supervisor or operator, is unsupported, no
  longer applies, or inherently violates a binding invariant.
- **Needs information:** available evidence cannot support either conclusion.

For valid requests, state readiness for prioritization or the information still
needed to scope them. Evaluate compatibility and risk only as far as evidence
supports: the CLI and JSON output, config and environment variables, state and
manifest schemas, `virt-install` arguments and cloud-init user-data, guest
isolation, network mode, image provenance, where host tools run, supported
distros and variants, and release behavior. Do not turn triage into speculative
implementation design or implement the request.

Ask only targeted questions that affect validity, ownership, observable
behavior, compatibility, acceptance, or rollout. Never request credentials,
private keys, registry tokens, GitHub tokens, customer data, or the contents of
a guest disk.

## Record And Finish

When external updates are authorized, post a concise evidence-based outcome and
apply only the repository's existing issue taxonomy. Leave valid and
needs-information requests open unless separately authorized. Close invalid
requests only with explicit closure authority; confirm duplicates before
linking them. Never promise priority, implementation, or a release.

Without mutation authority, report proposed comments, classifications,
questions, labels, and closure decisions without changing the tracker. Reconcile
the final queue with the initial inventory and summarize every issue's ID,
title, classification, evidence, information gaps, ownership, compatibility,
next action, and tracker state. State explicitly whether anything remains
unreviewed or needs information.
