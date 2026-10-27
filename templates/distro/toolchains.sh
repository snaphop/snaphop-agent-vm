# Put the per-account Go and Rust install directories on PATH.
#
# The toolchains themselves are not here. Go comes from mise, whose commands
# have a symlink each to the mise binary in /usr/local/bin; Rust lives in the
# shared /usr/local/rustup, whose proxies are symlinked into /usr/local/bin as
# well. That directory is already on the default PATH -- so `go`, `cargo` and
# `rustc` work in a non-interactive `ssh <vm> cargo build` too, which never
# reads this file.
#
# The shared Rust home belongs to the rustup group, every directory in it is
# setgid and group-writable, and every interactive account is in that group. So `rustup toolchain install`, and
# the install cargo starts for a project's rust-toolchain.toml, work without
# sudo and land where every account finds them. RUSTUP_HOME comes from
# /etc/environment, not from here, for the same non-interactive reason.
#
# What does need a PATH entry is what a user installs afterwards. `go install`
# writes to $HOME/go/bin (GOPATH defaults to ~/go) and `cargo install` writes
# to $HOME/.cargo/bin (CARGO_HOME defaults to ~/.cargo); both are per account
# and writable, which is why neither variable is set image-wide.
PATH="$HOME/go/bin:$HOME/.cargo/bin:$PATH"
export PATH
