# Put the per-account Go and Rust install directories on PATH.
#
# The toolchains themselves are not here. Go comes from mise, whose commands
# have a symlink each to the mise binary in /usr/local/bin; Rust lives in the
# shared /usr/local/rustup, whose proxies are symlinked into /usr/local/bin as
# well. That directory is already on the default PATH -- so `go`, `cargo` and
# `rustc` work in a non-interactive `ssh <vm> cargo build` too, which never
# reads this file.
#
# What does need a PATH entry is what a user installs afterwards. `go install`
# writes to $HOME/go/bin (GOPATH defaults to ~/go) and `cargo install` writes
# to $HOME/.cargo/bin (CARGO_HOME defaults to ~/.cargo); both are per account
# and writable, which is why neither variable is set image-wide.
PATH="$HOME/go/bin:$HOME/.cargo/bin:$PATH"
export PATH
