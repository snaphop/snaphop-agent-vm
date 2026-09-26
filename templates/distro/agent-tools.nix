# The guest tooling every agent-vm nix image installs (ADR-0012).
#
# This file is the whole point of the nix variant. The full recipes state the
# same tool set three times -- once in apt, once in dnf, once in pacman -- plus
# a fourth time in mise for the language toolchains. Here it is stated once,
# and the three <family>-nix.Containerfile recipes differ only in the packages
# that make a container image boot as a VM.
#
# What is deliberately NOT here:
#
#   - The boot layer. The kernel, initramfs, init, cloud-init, sshd, sudo,
#     chrony and qemu-guest-agent still come from the distro, because those are
#     what make the image bootable and reachable and they are the guest
#     contract docs/cli.md promises. A nix profile cannot replace them.
#   - Docker and the nested virtualization stack. Both are system daemons with
#     kernel-side state; nix can supply the binaries but not a running,
#     socket-activated daemon integrated with the distro's units, so the nix
#     recipes install those two from the distro as well.
#   - codex, which comes from OpenAI's installer, and pi, herdr, agy and grok,
#     which come from mise. None of them is packaged in nixpkgs, and none of
#     them was ever installed by a distro package manager, so nix mode does
#     not change how they arrive. See the recipes.
#
# Nothing here is a credential and nothing here is per-VM: a base image is
# shared by every VM built on it (SECURITY.md).
let
  # The nixpkgs this image is built from.
  #
  # A branch name is not a pin: it moves, so two builds a week apart install
  # different versions. scripts/pin-nixpkgs.sh resolves this to a commit
  # revision and its sha256 and rewrites the two bindings below, which is what
  # makes a rebuild reproducible. Until it has been run the fetch follows the
  # release branch, which is why manifest.json records the source digest rather
  # than promising a byte-for-byte reproducible rebuild (ADR-0006).
  nixpkgsRef = "nixos-25.05";
  nixpkgsSha256 = null;

  nixpkgs = builtins.fetchTarball ({
    url = "https://github.com/NixOS/nixpkgs/archive/${nixpkgsRef}.tar.gz";
  } // (if nixpkgsSha256 == null then { } else { sha256 = nixpkgsSha256; }));

  # claude-code and a few of the toolchains are unfree. This image exists to
  # run those agents, so refusing them would leave nothing to install.
  pkgs = import nixpkgs { config.allowUnfree = true; };

  # Resolve a list of attribute names, failing with all the missing ones at
  # once. nix's own error names only the first and says nothing about why it is
  # missing, which for a pinned tree is almost always "renamed since the pin".
  need = names:
    let
      missing = builtins.filter (name: !(pkgs ? ${name})) names;
    in
    if missing == [ ] then
      map (name: pkgs.${name}) names
    else
      throw ''
        agent-tools.nix: nixpkgs ${nixpkgsRef} has no ${builtins.concatStringsSep ", " missing}.
        Either the attribute was renamed upstream or the pin moved. Fix the name
        here, or re-pin with scripts/pin-nixpkgs.sh.
      '';

  # The tools an agent expects to find on a working machine. The full and slim
  # recipes install the same set from the distro; these are the nixpkgs names
  # for it.
  commonTooling = need [
    "bashInteractive"
    "coreutils"
    "findutils"
    "diffutils"
    "gnugrep"
    "gnused"
    "gawk"
    "gnutar"
    "gzip"
    "xz"
    "zip"
    "unzip"
    "curl"
    "wget"
    "rsync"
    "openssh"
    "git"
    "jq"
    "ripgrep"
    "fd"
    "fzf"
    "tree"
    "file"
    "less"
    "vim"
    "nano"
    "tmux"
    "htop"
    "procps"
    "psmisc"
    "which"
    "man-db"
    "cacert"
    "iputils"
    "traceroute"
    "dnsutils"
    "netcat-openbsd"
  ];

  # A compiler and the headers a build needs. build-essential, gcc/make and
  # base-devel are what the other three recipes install for this.
  buildTooling = need [
    "gcc"
    "gnumake"
    "binutils"
    "pkg-config"
  ];

  # The language toolchains. In the full recipes these come from mise, rustup
  # and the distro; here they are ordinary packages like everything else.
  #
  # Rust arrives as rustc and cargo rather than as rustup, which is the one
  # place this variant deliberately differs from the full image. rustup manages
  # a toolchain of its own under a RUSTUP_HOME, and the full recipe shares one
  # out of /usr/local -- so shipping rustup here would give a guest a rustup
  # pointing at a directory this image never creates, and would make `agent-vm
  # update`'s rustup step, which is gated only on the command existing, run
  # against it. Taking the toolchain from nixpkgs keeps it pinned with
  # everything else and lets that step correctly skip. rustfmt and clippy are
  # separate derivations here, and `cargo fmt` and `cargo clippy` need both.
  toolchains = need [
    "go"
    "rustc"
    "cargo"
    "rustfmt"
    "clippy"
    "nodejs_22"
    "python3"
    "temurin-bin"
    "maven"
    "golangci-lint"
  ];

  # The coding agents nixpkgs packages. The other four arrive in the recipes;
  # see the note at the top of this file.
  agents = need [
    "claude-code"
    "opencode"
  ];

  # nixos-25.05's wrangler 4.17.0 currently has a stale fixed-output hash for
  # its pnpm dependency tree. Keep this override narrow so the image can build
  # while the rest of the tooling still comes from the selected nixpkgs tree.
  wranglerFixed = pkgs.wrangler.overrideAttrs (old: {
    pnpmDeps = old.pnpmDeps.overrideAttrs (_: {
      outputHash = "sha256-U98sZ8Hg44tzh/KUQ4e6am5MLvr+5732B6fTfbtY4qE=";
    });
  });

  # gh and tea are how an agent works with a forge from inside the guest, and
  # wrangler is what the full image ships for Cloudflare deployments.
  forgeTooling = need [
    "gh"
    "tea"
  ] ++ [ wranglerFixed ];

  # nix itself has to be in the profile: it is what the guest uses to install
  # anything else, and the recipes take the nix-daemon units out of this
  # package's store path.
  nixItself = need [ "nix" ];
in
{
  # The profile the recipes install into /nix/var/nix/profiles/default. It is a
  # single closure so the profile can be set atomically rather than built up by
  # a sequence of nix-env installs that can half-fail.
  #
  # ignoreCollisions is on because a set this size always has some: several
  # packages ship the same man page or the same share/ file, and which copy
  # wins does not matter for any of them.
  env = pkgs.buildEnv {
    name = "agent-vm-tools";
    paths = commonTooling
      ++ buildTooling
      ++ toolchains
      ++ agents
      ++ forgeTooling
      ++ nixItself;
    ignoreCollisions = true;
    extraOutputsToInstall = [ "man" "doc" ];
  };

  # The browser set Playwright pins, built separately because the recipes point
  # PLAYWRIGHT_BROWSERS_PATH at this store path rather than linking it into the
  # profile. There is exactly one Chromium in the image, and this is it -- the
  # same promise docs/cli.md makes for the full images, kept the same way.
  browsers = pkgs.playwright-driver.browsers;
}
