# 23. Install Go, its tools, and Maven on the runner image

Date: 2026-10-23

## Status

Accepted

Extends [ADR-0013](./0013-self-hosted-github-actions-runner-variant.md) and
[ADR-0022](./0022-install-java-and-rust-on-the-runner-image.md). The runner
variant stays the slim recipe plus one shared section. That section also
installs the newest stable Go, golangci-lint, govulncheck, goimports,
gotestsum, and the newest Maven.

## Context

The organization's workflows build Go projects, this repository among them,
and Java projects with Maven. On a runner image a job had to install Go,
its linters, and Maven itself before it could build. `actions/setup-go` and
`actions/setup-java` do that on every job, and download again each time a
cache misses.

The full image ships Go, golangci-lint, and Maven through mise, and
`agent-vm update` moves them with `mise upgrade`. A runner image has no
mise. The distro packages are no substitute: Ubuntu's `golang-go`, Fedora's
`golang`, and Arch's `go` are different releases, often a year or more
apart, and a toolchain older than a module's `go` directive cannot build it
without downloading a newer one first. Not every family packages
golangci-lint, and the packaged Maven versions differ too.

go.dev publishes every Go release, newest first, as JSON at
`https://go.dev/dl/?mode=json`, with the SHA-256 of each archive. Since Go
1.21, the toolchain itself also downloads a newer one when a module's
`go.mod` asks for it (`GOTOOLCHAIN=auto`, the default in each release's
`go.env`). golangci-lint publishes a binary and a checksums file with each
GitHub release, and its maintainers do not support building it with
`go install`. Maven Central lists every `apache-maven` version in
`maven-metadata.xml` and publishes a SHA-512 beside each archive. That
listing's `<release>` element can name a release candidate.

## Decision

- Two scripts join the shared runner section. They are the same on Ubuntu,
  Fedora, and Arch. `templates/distro/runner-maven.sh` runs after
  `runner-java.sh`, because Maven runs on that JDK.
  `templates/distro/runner-go.sh` runs after `runner-rust.sh` and before
  Docker.
- **Go.** The newest stable release and its Linux archive for the host
  architecture (`amd64` or `arm64`), checked against the SHA-256 go.dev
  publishes, extracted beside `/usr/local/go`, then swapped in. `go` and
  `gofmt` are linked into `/usr/local/bin`.
- **Go tools.** golangci-lint from the newest GitHub release, checked
  against that release's checksums file. govulncheck
  (`golang.org/x/vuln`), goimports (`golang.org/x/tools`), and gotestsum
  (`gotest.tools/gotestsum`) at their modules' newest versions, built with
  `go install` and the Go just installed (`GOTOOLCHAIN=local`), with a
  scratch module and build cache that is removed. All four go into
  `/usr/local/lib/go-tools/bin`, outside `/usr/local/go`, so replacing Go
  does not remove them, and are linked into `/usr/local/bin`.
- **Nothing in Go is pinned, and nothing is written to the environment.** Go
  finds its root through the links, so `GOROOT`, `GOPATH`, and `GOTOOLCHAIN`
  are not written to `/etc/environment` or the runner's `.env`. `GOPATH`,
  the module cache, and any toolchain a `go.mod` asks for stay in each
  account's own `~/go`.
- **`agent-vm update` keeps Go and its tools current.** `runner-go.sh`
  stays in the image as `/usr/local/sbin/agent-vm-runner-go`, linked into
  `/usr/sbin`. `agent-vm update` gains one step, run as root only when the
  guest has that command: it runs the script again. Go and each tool are
  replaced only when a newer version exists, so a current guest downloads
  only the version listings. A failed download or check leaves the
  installed Go in place.
- **Maven.** The newest plain `X.Y.Z` in Maven Central's listing, not its
  `<release>` element, checked against the published SHA-512 and extracted
  to `/opt/maven`. `mvn` and `mvnDebug` are linked into `/usr/local/bin`.
  `mvn` finds its home through the link, so `MAVEN_HOME` is not set. Like
  the JDK, Maven moves when the image is rebuilt, not on `agent-vm update`.
- **Checked as the job account.** Before the image is committed, and after
  each update for Go, the scripts run `go`, `gofmt`, each Go tool, and
  `mvn -version` as `github-runner`, with `HOME=/opt/actions-runner`, the
  `PATH` a job gets, and for Maven the `JAVA_HOME` from the runner's `.env`.
  The build fails if `go` does not report the release installed or `mvn`
  does not report the Maven version installed.
- No new host tool, no manifest change, and no change to `configure`.

## Consequences

Easier:

- A Go or Maven workflow starts building as soon as it checks out, and
  `actions/setup-go` finds a current Go already on `PATH`.
- golangci-lint, govulncheck, goimports, and gotestsum are on `PATH` for
  every job.
- `agent-vm update` keeps a runner VM on the newest Go and Go tools without
  a rebuild.
- A module whose `go` directive is newer than the installed release still
  builds; Go downloads the toolchain it names into that account's `~/go`.

Harder:

- The runner image is larger by a Go toolchain, four tools, and Maven, and
  its build and a runner guest's update are longer by the time it takes to
  build three of the tools.
- Nothing here is pinned. A rebuild, or for Go an update, picks up a new
  release. A workflow that needs a particular golangci-lint version installs
  it. The manifest records only the source image's digest, so a rebuild is
  not reproducible (ADR-0006).
- The image build contacts go.dev, the Go module proxy, GitHub, and Maven
  Central. `agent-vm update` on a runner guest contacts the first three.
- `agent-vm update` moves Go but not Maven. A new Maven reaches a runner VM
  when its image is rebuilt and the VM is created again.
- An image already cached gains none of this until it is rebuilt with
  `agent-vm image build <family>-runner --force`. `agent-vm update` on a VM
  from an older image skips the Go step, because the guest has no
  `agent-vm-runner-go`.
