package hostexec

import (
	"context"
	"strings"
	"testing"
)

const testApplianceDir = "/var/lib/agent-vm/appliance-kernel/6.8.0-31-generic"

func TestApplianceKernel_SetsSuperminEnvironmentOnLibguestfsTools(t *testing.T) {
	fake := NewFake()
	runner := NewApplianceKernel(fake, func() string { return testApplianceDir })

	if _, err := runner.Run(context.Background(), Command{
		Name:   VirtMakeFS.Name,
		Args:   []string{"--type=ext4", "/tmp/rootfs.tar", "/tmp/base.qcow2"},
		Effect: Mutate,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	calls := fake.Calls()
	if len(calls) != 1 {
		t.Fatalf("ran %d commands, want 1: %s", len(calls), fake)
	}
	want := []string{
		"SUPERMIN_KERNEL=" + testApplianceDir + "/Image",
		"SUPERMIN_MODULES=" + testApplianceDir + "/modules",
		"SUPERMIN_KERNEL_VERSION=6.8.0-31-generic",
	}
	got := strings.Join(calls[0].Env, " ")
	for _, w := range want {
		if !strings.Contains(got, w) {
			t.Errorf("environment %q is missing %q", got, w)
		}
	}
	// The argument vector is a public contract and must not move because of
	// where the appliance kernel came from.
	if strings.Join(calls[0].Argv(), " ") != "virt-make-fs --type=ext4 /tmp/rootfs.tar /tmp/base.qcow2" {
		t.Errorf("argv changed: %v", calls[0].Argv())
	}
}

// Only libguestfs reads these variables. Setting them on qemu-img or
// virt-install would put three lines of noise on every --dry-run and imply a
// dependency that is not there.
func TestApplianceKernel_LeavesOtherToolsAlone(t *testing.T) {
	fake := NewFake()
	runner := NewApplianceKernel(fake, func() string { return testApplianceDir })

	for _, tool := range []string{QemuImg.Name, VirtInstall.Name, Virsh.Name, Podman.Name} {
		if _, err := runner.Run(context.Background(), Command{Name: tool, Effect: Read}); err != nil {
			t.Fatalf("Run %s: %v", tool, err)
		}
	}
	for _, c := range fake.Calls() {
		if len(c.Env) != 0 {
			t.Errorf("%s was given an environment: %v", c.Name, c.Env)
		}
	}
}

// A host whose own kernel boots the appliance configures nothing, and its
// commands must come out exactly as they went in.
func TestApplianceKernel_UnconfiguredLeavesCommandsUntouched(t *testing.T) {
	fake := NewFake()
	runner := NewApplianceKernel(fake, func() string { return "" })

	if _, err := runner.Run(context.Background(), Command{Name: VirtMakeFS.Name, Effect: Mutate}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := fake.Calls()[0]; len(got.Env) != 0 {
		t.Errorf("virt-make-fs was given an environment: %v", got.Env)
	}
}

func TestApplianceKernel_RenderShowsTheEnvironmentItWouldRunWith(t *testing.T) {
	runner := NewApplianceKernel(NewFake(), func() string { return testApplianceDir })

	got := runner.Render(Command{Name: VirtLs.Name, Args: []string{"-a", "/tmp/base.qcow2", "/boot"}, Effect: Read})

	if !strings.HasPrefix(got, "SUPERMIN_KERNEL="+testApplianceDir+"/Image ") {
		t.Errorf("--dry-run would print %q, which does not lead with the environment", got)
	}
	if !strings.HasSuffix(got, "virt-ls -a /tmp/base.qcow2 /boot") {
		t.Errorf("--dry-run would print %q, which does not end with the command", got)
	}
}

// Over the ssh transport the environment has to reach the far side, and the
// only thing that carries it is the remote command line.
func TestApplianceKernel_EnvironmentSurvivesTheSSHTransport(t *testing.T) {
	fake := NewFake()
	remote := NewRemote(fake, nil, "kvm@hv.example.com", "")
	runner := NewApplianceKernel(remote, func() string { return testApplianceDir })

	if _, err := runner.Run(context.Background(), Command{
		Name: VirtSysprep.Name, Args: []string{"-a", "/srv/base.qcow2"}, Effect: Mutate,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	calls := fake.Calls()
	if len(calls) != 1 {
		t.Fatalf("ran %d commands, want 1: %s", len(calls), fake)
	}
	remoteCmd := calls[0].Args[len(calls[0].Args)-1]
	want := "exec env SUPERMIN_KERNEL=" + testApplianceDir + "/Image " +
		"SUPERMIN_MODULES=" + testApplianceDir + "/modules " +
		"SUPERMIN_KERNEL_VERSION=6.8.0-31-generic virt-sysprep -a /srv/base.qcow2"
	if remoteCmd != want {
		t.Errorf("remote command =\n  %q\nwant\n  %q", remoteCmd, want)
	}
	// The ssh invocation itself must not carry the variables: they belong to
	// the tool on the far side, not to ssh here.
	if len(calls[0].Env) != 0 {
		t.Errorf("ssh was given an environment: %v", calls[0].Env)
	}
}

func TestApplianceKernelEnv_DerivesTheVersionFromTheDirectoryName(t *testing.T) {
	got := ApplianceKernelEnv("/opt/kernels/6.12.4-arch1-1")

	if len(got) != 3 {
		t.Fatalf("got %d variables, want 3: %v", len(got), got)
	}
	if got[2] != "SUPERMIN_KERNEL_VERSION=6.12.4-arch1-1" {
		t.Errorf("version = %q, want it taken from the directory name", got[2])
	}
}
