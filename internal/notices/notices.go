// Package notices holds the license texts that travel with every copy of the
// agent-vm binary.
//
// LICENSE is this project's MIT license. NOTICE carries the copyright and
// permission notices of the software linked into the binary: BurntSushi/toml,
// and the Go standard library and runtime, including the patent grant
// distributed with Go. Both files are embedded so the text is present in the
// binary itself, which is how a single static binary carries the notices. They
// are copies of the files at the repository root, which a release attaches
// beside the binaries; notices_test.go fails if the two copies drift.
//
// The licenses command references these variables. An unreferenced embed is
// dropped by the linker, and the binary would then no longer carry the notices.
// The Go text was copied from the LICENSE and PATENTS files shipped with Go
// 1.27.1, and the toml text from that module's COPYING file at v1.6.0. Refresh
// them when those upstream files change.
package notices

import _ "embed"

//go:embed LICENSE
var License string

//go:embed NOTICE
var Notice string
