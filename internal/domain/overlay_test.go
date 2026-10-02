package domain

import (
	"context"
	"strings"
	"testing"

	"github.com/snaphop/snaphop-agent-vm/internal/config"
	"github.com/snaphop/snaphop-agent-vm/internal/hostexec"
)

func TestCreateOverlay_BacksTheNewDiskWithTheImmutableBase(t *testing.T) {
	fake := hostexec.NewFake()

	err := manager(fake).CreateOverlay(context.Background(),
		"/state/images/ubuntu/24.04/base.qcow2",
		"/state/vms/agent-01/root.qcow2",
		50*config.GiB)
	if err != nil {
		t.Fatalf("CreateOverlay: %v", err)
	}

	want := "qemu-img create -f qcow2 -F qcow2 -b /state/images/ubuntu/24.04/base.qcow2 " +
		"/state/vms/agent-01/root.qcow2 53687091200"
	if !fake.Ran(want) {
		t.Errorf("unexpected invocation:\n%s\nwant: %s", fake, want)
	}
	// Nothing may rewrite the base image: it is shared by every VM built on it.
	for _, argv := range fake.Argvs() {
		if strings.Contains(argv, "rebase") || strings.Contains(argv, "commit") {
			t.Errorf("the base image must never be modified: %v", argv)
		}
	}
}

func TestCreateOverlay_RejectsARelativePath(t *testing.T) {
	fake := hostexec.NewFake()

	err := manager(fake).CreateOverlay(context.Background(),
		"images/ubuntu/24.04/base.qcow2", "/state/vms/agent-01/root.qcow2", 50*config.GiB)
	if err == nil {
		t.Fatal("want a refusal: a relative backing path resolves differently for qemu than for us")
	}
	if len(fake.Calls()) != 0 {
		t.Errorf("nothing may run when the paths are invalid:\n%s", fake)
	}
}

func TestInspectDisk_ReadsRealQemuImgOutput(t *testing.T) {
	fake := hostexec.NewFake()
	fake.RespondPrefix("qemu-img info -U --output=json", hostexec.FakeResponse{
		Stdout: toolout(t, "qemu-img-info-json-overlay.json"),
	})

	info, err := manager(fake).InspectDisk(context.Background(), "/tmp/agent-vm-capture/root.qcow2")
	if err != nil {
		t.Fatalf("InspectDisk: %v", err)
	}
	if info.VirtualSize != 50*config.GiB {
		t.Errorf("virtual size = %s, want 50G", info.VirtualSize)
	}
	// The overlay holds almost nothing until the guest writes: that difference
	// is the whole reason a VM is created in seconds.
	if info.ActualSize >= config.MiB {
		t.Errorf("actual size = %s, want a nearly empty overlay", info.ActualSize)
	}
	if info.BackingFile != "/tmp/agent-vm-capture/base.qcow2" {
		t.Errorf("backing file = %q, want the base image", info.BackingFile)
	}
}

func TestInspectDisk_AsksQemuImgToIgnoreTheRunningDomainsLock(t *testing.T) {
	// A running domain holds a write lock on its overlay. Without -U, qemu-img
	// refuses to open it at all, so `agent-vm info` loses its disk detail for
	// exactly the VMs someone is most likely to be asking about.
	fake := hostexec.NewFake()
	fake.RespondPrefix("qemu-img info", hostexec.FakeResponse{
		Stdout: toolout(t, "qemu-img-info-json-overlay.json"),
	})

	if _, err := manager(fake).InspectDisk(context.Background(), "/state/vms/agent-01/root.qcow2"); err != nil {
		t.Fatalf("InspectDisk: %v", err)
	}
	if !strings.Contains(fake.String(), "qemu-img info -U ") {
		t.Errorf("qemu-img was not asked to override the lock:\n%s", fake)
	}
}

func TestInspectDisk_UnreadableOutputIsAnError(t *testing.T) {
	fake := hostexec.NewFake()
	fake.RespondPrefix("qemu-img info", hostexec.FakeResponse{Stdout: "qemu-img: Could not open"})

	if _, err := manager(fake).InspectDisk(context.Background(), "/state/vms/agent-01/root.qcow2"); err == nil {
		t.Fatal("want an error rather than a zero-valued disk record")
	}
}
