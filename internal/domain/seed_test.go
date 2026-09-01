package domain

import (
	"context"
	"strings"
	"testing"

	"git.snaphop.xyz/snaphop/snaphop-agent-vm/internal/hostexec"
)

const (
	seedDir  = "/home/operator/.local/share/agent-vm/vms/agent-01/seed"
	seedFile = "/home/operator/.local/share/agent-vm/vms/agent-01/seed.img"
)

// The label is cloud-init's to require, not ours to choose: NoCloud searches
// for a filesystem called cidata and finds nothing without it, which is the
// same silent "no datasource" failure a late-appearing seed produced.
func TestCreateSeed_BuildsALabelledFilesystemFromTheSeedDirectory(t *testing.T) {
	fake := hostexec.NewFake()

	if err := manager(fake).CreateSeed(context.Background(), seedDir, seedFile); err != nil {
		t.Fatalf("CreateSeed: %v", err)
	}

	want := "virt-make-fs --type=vfat --label=cidata --size=8M --format=raw " + seedDir + " " + seedFile
	if got := strings.Join(fake.Argvs(), "\n"); got != want {
		t.Errorf("argv =\n%s\nwant\n%s", got, want)
	}
}

// A relative path would be resolved against whatever directory the tool
// happened to run in — and with a remote hypervisor, on the wrong machine.
func TestCreateSeed_RejectsRelativePaths(t *testing.T) {
	for _, tt := range []struct{ name, dir, image string }{
		{"relative seed directory", "seed", seedFile},
		{"relative seed image", seedDir, "seed.img"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			fake := hostexec.NewFake()
			if err := manager(fake).CreateSeed(context.Background(), tt.dir, tt.image); err == nil {
				t.Error("CreateSeed accepted a relative path")
			}
			if len(fake.Argvs()) != 0 {
				t.Errorf("CreateSeed ran something anyway: %v", fake.Argvs())
			}
		})
	}
}
