# 15. Register a GitHub Actions runner with gh

Date: 2026-10-08

## Status

Accepted

Narrows [ADR-0013](./0013-self-hosted-github-actions-runner-variant.md). The
runner image, the in-guest command, and the rule that a base image carries no
token stay as they are. This decision adds an optional registration at create
time, and removal at destroy time, for a runner this tool registered.

The credential follows the path in
[ADR-0014](./0014-join-a-tailscale-network-from-the-guest.md): a short-lived
secret crosses into the guest on stdin after SSH, and nowhere else.

## Context

A runner image installs GitHub's self-hosted runner and leaves it
unconfigured (ADR-0013). The operator then runs
`agent-vm-github-runner configure` inside the guest with a registration
token. That path stays available when no organization is named.

When the client already has `gh` logged in, the same registration can be done
for the operator. GitHub's organization endpoint mints a short-lived token:

```text
gh api --method POST orgs/<org>/actions/runners/registration-token --jq .token
```

The token cannot travel the way an SSH public key does. `SECURITY.md` forbids
a GitHub token in a base image, a cloud-init seed, generated user-data,
`vm.json`, a log, or an argument vector. A base image is shared by every VM
built from it. The seed sits on disk for the life of the VM. Argument vectors
are logged. The token is also single purpose and short lived, so fetching it
before a long image build would hand the guest a token that has already
expired.

The organization has to be named. A host that has `gh` may be logged into an
account that belongs to several organizations, and guessing would register the
runner in the wrong one. `create --distro ubuntu-runner` with no organization
keeps producing an unregistered runner.

The runner's id comes from GitHub, not from the guest. The guest is
untrusted. An id written into the guest's `.runner` file could make a later
destroy delete a different runner.

## Decision

When `create` uses a `-runner` image and an organization is set, `create`
registers that guest as an organization Actions runner after SSH accepts a
login. `gh` 2.0 or newer runs on the client.

- **The organization is explicit.** `--github-org`, `[github] org` in the
  config file, and `AGENT_VM_GITHUB_ORG` set it, in that precedence. An empty
  value is unset. The name is a GitHub login: 1 to 39 characters, letters,
  digits, and hyphens, and it does not start or end with a hyphen.
  github.com only. An explicit `--github-org` on an image that is not a
  `-runner` variant is a usage error. A config or environment organization on
  such an image is ignored, so a default organization does not change an
  ordinary create.
- **The token is fetched on the client, after SSH.** The command is
  `gh api --method POST orgs/<org>/actions/runners/registration-token --jq .token`.
  Before the VM directory is created, `create` checks that `gh` is present
  and that its login can manage runners. When `gh auth status` lists scopes,
  one of `admin:org` or `manage_runners:org` is required. A login with no
  scope line is left for the API to accept or refuse. The token itself is
  fetched only after the guest is reachable. A dry run prints the command and
  does not fetch a token.
- **The token crosses only on stdin.** The guest command is
  `sudo -n /usr/local/sbin/agent-vm-github-runner configure --url https://github.com/<org> --name <vm> --token-file - --replace`.
  The token is the raw value, with no added newline, on that command's
  stdin. It is not a flag value, not an environment variable, not an
  argument, and it is not written into the image, the seed, generated
  user-data, `vm.json`, or a log. The type that holds it redacts itself from
  string and debug output, and from a tool error's stderr. The operator's
  `gh` credential stays on the client. `gh` is not authenticated inside the
  guest.
- **The runner name is the VM name.** No labels, runner group, or ephemeral
  flag are set. `--replace` lets a recreated VM of the same name take the
  place of a runner GitHub still has. The in-guest command is the one
  ADR-0013 already installs. This decision does not change that script.
- **The record holds no token.** After configure returns, the id is read with
  `gh api --paginate orgs/<org>/actions/runners --jq '.runners[] | select(.name == "<vm>") | .id'`.
  Exactly one positive id is accepted. `vm.json` gains an optional
  `githubRunner` object: `org`, `name`, `id` (omitted when it is 0), and
  `url` (`https://github.com/<org>`). `schemaVersion` stays 1. `create` and
  `info` show `org/name (id N)`. `list` is unchanged. When configure succeeded
  and the id cannot be read, the object is still saved with no id, the VM
  stays, and the error says destroy will look the runner up by name.
- **A failure after the VM is recorded leaves the VM in place,** as a
  Tailscale join or a GitHub SSH-key failure does. Registration runs after
  those two, when they were also requested. `--wait-for-ssh 0` together with
  an organization that would register is a usage error, because there is no
  SSH session to register through.
- **Destroy removes a runner this tool registered.** When `githubRunner` is
  present, destroy runs
  `gh api --method DELETE orgs/<org>/actions/runners/<id> --silent`
  before the domain is undefined, after the optional SSH-key removal. HTTP
  404 means the runner is already gone and destroy continues. Any other `gh`
  failure leaves the domain and the record. The record is cleared before
  undefine, so a later failure retries a runner that is already gone. A
  recorded id of 0 is looked up by name first. No runner of that name is
  treated as already gone. More than one match is refused, so destroy does
  not delete the wrong runner. A runner the operator registered by hand,
  which has no record, stays at GitHub. `--keep-disk` still removes the
  recorded runner. A cancelled confirmation does not call GitHub. A dry run
  prints the DELETE and does not change the record.
- **`gh` stays optional.** `doctor` does not fail a host that lacks it. A
  create that would register, and a destroy of a recorded runner, require
  `gh` 2.0 or newer and exit 3 when it is missing or too old. A login whose
  listed scopes cannot manage runners exits 1 before anything is created, or
  before a recorded runner is deleted, and names
  `gh auth refresh -h github.com -s admin:org`.

Creating a runner image with no organization is unchanged: the guest boots
unregistered, and the operator runs `agent-vm-github-runner configure` by
hand.

## Consequences

Easier:

- `agent-vm create ci --distro ubuntu-runner --github-org <org>` registers
  the runner after boot, using the login `gh` already has.
- `agent-vm destroy ci` removes that registration before the domain is
  undefined, so a thrown-away runner does not stay in the organization.
- The image, the seed, and generated user-data still contain no token.
  Cached runner images stay bootable. The manual configure command remains.

Harder:

- The guest can read the token while `config.sh` runs. The token is
  short-lived and can register a runner. The operator's `gh` credential does
  not enter the guest. `config.sh` still receives the token as an argument
  inside the guest, which is the interface that program ships (ADR-0013).
- Registration needs a boot wait, a reachable guest, and a `gh` login that
  can manage the organization's runners.
- A runner registered by hand has no record, and destroy leaves it at GitHub.
- The id is matched by name. Two runners with the same name in one
  organization stop destroy rather than deleting one of them.
