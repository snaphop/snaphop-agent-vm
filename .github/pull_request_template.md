## What Changed

<!-- What this PR does, in a sentence or two. -->

## Why

<!-- The problem or request behind it. Link the issue if there is one. -->

## How It Was Verified

```text
<!-- The exact commands you ran, and their outcome. -->
```

- Distros exercised: <!-- ubuntu / fedora / arch / none -->
- Network modes exercised: <!-- nat / bridge / none -->
- Not verified: <!-- state this plainly rather than leaving it implied -->

<!-- For VM-affecting changes, include the relevant evidence: `--dry-run` output, a
`virsh dumpxml` excerpt, guest console output, or `agent-vm doctor` output. -->

## Operational And Compatibility Impact

<!-- Host requirements (new tool? raised minimum version?), base image rebuild
needed, state or schema changes, default changes, anything an existing user has to
do. "None" is a valid answer. -->

## Checklist

- [ ] `gofmt -l .` is empty; `go vet ./...`, `golangci-lint run`, and
      `go test ./...` pass.
- [ ] New behavior has tests; bug fixes have regression coverage that does not
      depend on the optional integration suite.
- [ ] Golden-file diffs (`virt-install` argv, cloud-init user-data) are intentional
      and explained above.
- [ ] Nothing reimplements what `virt-install`, `virsh`, `podman`, `qemu-img`,
      `cloud-localds`, or libguestfs already does; no `os/exec` outside
      `internal/hostexec`; machine-readable output modes used where available.
- [ ] Any new tool dependency or raised version floor is reflected in `doctor`, the
      dependency tables, `docs/host-setup.md`, and `docs/cli.md`.
- [ ] Public contracts synchronized: `docs/cli.md` (including **Underlying
      Commands**), `--help`, config keys, env vars, and any `schemaVersion` bump
      with its rebuild/upgrade path.
- [ ] `CHANGELOG.md` updated under `[Unreleased]` for notable behavior, security,
      dependency, compatibility, or deployment changes.
- [ ] An ADR added for any significant architectural decision (virtualization
      stack, boot method, image cache format, guest-to-host sharing, network
      modes, default resources, new distro family, reimplementing what a host
      tool already does).
- [ ] `SECURITY.md` boundaries respected — guest isolation, path containment,
      subprocess invocation, destroy path, image provenance.
- [ ] No credentials, private keys, PII, disk images, base images, build output,
      or unrelated formatting changes included.
