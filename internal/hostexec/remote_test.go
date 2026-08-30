package hostexec

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func newRemoteFake(t *testing.T) (*Remote, *Fake) {
	t.Helper()
	fake := NewFake()
	remote := NewRemote(fake, nil, "kvm@hv.example.com", "")
	return remote, fake
}

func TestRemote_WrapsHypervisorCommandsInSSH(t *testing.T) {
	remote, fake := newRemoteFake(t)

	if _, err := remote.Run(context.Background(), Command{
		Name: "qemu-img", Args: []string{"create", "-f", "qcow2", "/srv/agent-vm/vms/web/root.qcow2"},
		Effect: Mutate,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	calls := fake.Calls()
	if len(calls) != 1 {
		t.Fatalf("ran %d commands, want 1: %s", len(calls), fake)
	}
	got := calls[0]
	if got.Name != "ssh" {
		t.Errorf("ran %q, want ssh", got.Name)
	}
	argv := strings.Join(got.Argv(), " ")
	if !strings.Contains(argv, "kvm@hv.example.com") {
		t.Errorf("argv does not name the destination: %s", argv)
	}
	want := "exec qemu-img create -f qcow2 /srv/agent-vm/vms/web/root.qcow2"
	if got.Args[len(got.Args)-1] != want {
		t.Errorf("remote command = %q, want %q", got.Args[len(got.Args)-1], want)
	}
	if !strings.Contains(argv, "BatchMode=yes") {
		t.Errorf("argv does not refuse prompts: %s", argv)
	}
}

// gh and the ssh into a guest use the operator's own credentials and terminal,
// so they must run here rather than on the hypervisor.
func TestRemote_RunsClientCommandsLocally(t *testing.T) {
	remote, fake := newRemoteFake(t)

	if _, err := remote.Run(context.Background(), Command{
		Name: "gh", Args: []string{"auth", "status"}, Location: Client, Effect: Read,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	if !fake.Ran("gh auth status") {
		t.Errorf("client command was not run as itself: %s", fake)
	}
}

func TestRemote_ConnectionSharingSocket(t *testing.T) {
	fake := NewFake()
	remote := NewRemote(fake, nil, "hv", "/tmp/agent-vm-ssh-1")

	if _, err := remote.Run(context.Background(), Command{Name: "true", Effect: Read}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	argv := strings.Join(fake.Calls()[0].Argv(), " ")
	for _, want := range []string{"ControlMaster=auto", "ControlPath=/tmp/agent-vm-ssh-1/ssh", "ControlPersist=120"} {
		if !strings.Contains(argv, want) {
			t.Errorf("argv is missing %s: %s", want, argv)
		}
	}
}

func TestRemote_PassesPortIdentityAndNoVerify(t *testing.T) {
	fake := NewFake()
	remote := NewRemote(fake, nil, "hv", "")
	remote.Port = 2222
	remote.IdentityFile = "/home/me/.ssh/hv"
	remote.NoVerify = true

	if _, err := remote.Run(context.Background(), Command{Name: "true", Effect: Read}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	argv := strings.Join(fake.Calls()[0].Argv(), " ")
	for _, want := range []string{"-p 2222", "-i /home/me/.ssh/hv", "StrictHostKeyChecking=no"} {
		if !strings.Contains(argv, want) {
			t.Errorf("argv is missing %s: %s", want, argv)
		}
	}
}

// A working directory names a directory on the hypervisor, which this machine
// may not even have, so it is expressed in the remote command rather than
// applied to ssh.
func TestRemote_WorkingDirectoryIsRemote(t *testing.T) {
	remote, fake := newRemoteFake(t)

	if _, err := remote.Run(context.Background(), Command{
		Name: "podman", Args: []string{"build", "."}, Dir: "/srv/agent-vm/images/.build", Effect: Mutate,
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := fake.Calls()[0]
	if got.Dir != "" {
		t.Errorf("ssh was given Dir %q, which is a path on the other machine", got.Dir)
	}
	want := "cd /srv/agent-vm/images/.build && exec podman build ."
	if got.Args[len(got.Args)-1] != want {
		t.Errorf("remote command = %q, want %q", got.Args[len(got.Args)-1], want)
	}
}

// A failure has to name the tool the operator asked for, not ssh, and say
// which machine it ran on so they can rerun it there.
func TestRemote_ErrorNamesTheToolAndTheHost(t *testing.T) {
	fake := NewFake()
	fake.Default = FakeResponse{ExitCode: 1, Stderr: "qemu-img: could not open the image"}
	remote := NewRemote(fake, nil, "kvm@hv.example.com", "")

	_, err := remote.Run(context.Background(), Command{Name: "qemu-img", Args: []string{"info", "/x"}, Effect: Read})

	var toolErr *ToolError
	if !errors.As(err, &toolErr) {
		t.Fatalf("Run = %v, want a *ToolError", err)
	}
	if toolErr.Tool != "qemu-img" {
		t.Errorf("Tool = %q, want qemu-img", toolErr.Tool)
	}
	if toolErr.Host != "kvm@hv.example.com" {
		t.Errorf("Host = %q, want kvm@hv.example.com", toolErr.Host)
	}
	if !strings.Contains(err.Error(), "on kvm@hv.example.com") {
		t.Errorf("message does not say where it ran: %s", err)
	}
}

// A shell reports a command it cannot find by exiting 127, which is how a tool
// missing from the hypervisor reaches us.
func TestRemote_MissingRemoteToolIsNotFound(t *testing.T) {
	fake := NewFake()
	fake.Default = FakeResponse{ExitCode: 127, Stderr: "bash: line 1: virt-sysprep: command not found"}
	remote := NewRemote(fake, nil, "hv", "")

	_, err := remote.Run(context.Background(), Command{Name: "virt-sysprep", Effect: Read})

	var missing *NotFoundError
	if !errors.As(err, &missing) {
		t.Fatalf("Run = %v, want a *NotFoundError", err)
	}
	if missing.Tool != "virt-sysprep" {
		t.Errorf("Tool = %q, want virt-sysprep", missing.Tool)
	}
}

// ssh failing to connect and a tool failing on the far side need different
// remedies, so they must not be reported as the same thing.
func TestRemote_TransportFailureIsDistinct(t *testing.T) {
	fake := NewFake()
	fake.Default = FakeResponse{ExitCode: 255, Stderr: "kvm@hv: Permission denied (publickey)."}
	remote := NewRemote(fake, nil, "kvm@hv", "")

	_, err := remote.Run(context.Background(), Command{Name: "virsh", Effect: Read})

	var transport *TransportError
	if !errors.As(err, &transport) {
		t.Fatalf("Run = %v, want a *TransportError", err)
	}
	if !strings.Contains(err.Error(), "ssh kvm@hv true") {
		t.Errorf("message does not say how to fix it: %s", err)
	}
}

// A tool that genuinely exits 255 must not be mistaken for ssh failing to
// connect: its stderr is the tool's, not ssh's.
func TestRemote_ToolExiting255IsNotATransportFailure(t *testing.T) {
	fake := NewFake()
	fake.Default = FakeResponse{ExitCode: 255, Stderr: "virt-install: error: invalid argument"}
	remote := NewRemote(fake, nil, "hv", "")

	_, err := remote.Run(context.Background(), Command{Name: "virt-install", Effect: Read})

	var transport *TransportError
	if errors.As(err, &transport) {
		t.Fatalf("Run = %v, want a tool failure rather than a transport failure", err)
	}
	var toolErr *ToolError
	if !errors.As(err, &toolErr) || toolErr.Tool != "virt-install" {
		t.Fatalf("Run = %v, want a *ToolError naming virt-install", err)
	}
}

func TestRemote_TimeoutNamesTheToolAndTheHost(t *testing.T) {
	fake := NewFake()
	fake.Default = FakeResponse{Err: &TimeoutError{Tool: "ssh", Timeout: time.Minute}}
	remote := NewRemote(fake, nil, "hv", "")

	_, err := remote.Run(context.Background(), Command{Name: "podman", Effect: Read})

	var timeout *TimeoutError
	if !errors.As(err, &timeout) {
		t.Fatalf("Run = %v, want a *TimeoutError", err)
	}
	if timeout.Tool != "podman" || timeout.Host != "hv" {
		t.Errorf("TimeoutError = %+v, want podman on hv", timeout)
	}
}

// LookPath asks a shell builtin, which is the one invocation that must not be
// exec'd: exec would look for a program called "command" and report every tool
// as missing.
func TestRemote_LookPathDoesNotExecTheShellBuiltin(t *testing.T) {
	fake := NewFake()
	fake.Default = FakeResponse{Stdout: "/usr/bin/virsh\n"}
	remote := NewRemote(fake, nil, "hv", "")

	path, err := remote.LookPath("virsh", Hypervisor)
	if err != nil {
		t.Fatalf("LookPath: %v", err)
	}
	if path != "/usr/bin/virsh" {
		t.Errorf("LookPath = %q, want /usr/bin/virsh", path)
	}
	sent := fake.Calls()[0]
	if got := sent.Args[len(sent.Args)-1]; got != "command -v virsh" {
		t.Errorf("remote command = %q, want %q", got, "command -v virsh")
	}
}

func TestRemote_LookPathForAClientToolAsksThisMachine(t *testing.T) {
	remote, fake := newRemoteFake(t)

	if _, err := remote.LookPath("gh", Client); err != nil {
		t.Fatalf("LookPath: %v", err)
	}
	if len(fake.Calls()) != 0 {
		t.Errorf("a client tool was looked up over ssh: %s", fake)
	}
}

// virsh console has to reach the operator's terminal, so becoming the command
// on the hypervisor asks ssh for a terminal.
func TestRemote_BecomeAllocatesATerminal(t *testing.T) {
	remote, fake := newRemoteFake(t)

	if err := remote.Become(Command{Name: "virsh", Args: []string{"console", "web"}}); err != nil {
		t.Fatalf("Become: %v", err)
	}
	became := fake.Became()
	if len(became) != 1 {
		t.Fatalf("became %d commands, want 1", len(became))
	}
	if became[0].Name != "ssh" || became[0].Args[0] != "-t" {
		t.Errorf("became %v, want ssh -t …", became[0].Argv())
	}
}

// Becoming a command is the last thing the process does, so there is nothing
// left to share a connection with — and the caller removes the socket's
// directory first, which an invocation still asking for it would then fail on.
func TestRemote_BecomeDoesNotAskForTheSharedConnection(t *testing.T) {
	f := NewFake()
	remote := NewRemote(f, nil, "hv", "/tmp/agent-vm-ssh-1")

	if err := remote.Become(Command{Name: "virsh", Args: []string{"console", "web"}}); err != nil {
		t.Fatalf("Become: %v", err)
	}
	argv := strings.Join(f.Became()[0].Argv(), " ")
	if strings.Contains(argv, "ControlPath") || strings.Contains(argv, "ControlMaster") {
		t.Errorf("Become asked for the shared connection: %s", argv)
	}

	// Every other invocation still shares one.
	if _, err := remote.Run(context.Background(), Command{Name: "virsh", Effect: Read}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(strings.Join(f.Calls()[1].Argv(), " "), "ControlPath=/tmp/agent-vm-ssh-1/ssh") {
		t.Errorf("an ordinary invocation lost the shared connection: %s", f)
	}
}

func TestRemote_BecomeForAClientCommandIsUnwrapped(t *testing.T) {
	remote, fake := newRemoteFake(t)

	if err := remote.Become(Command{Name: "ssh", Args: []string{"agent@192.168.122.10"}, Location: Client}); err != nil {
		t.Fatalf("Become: %v", err)
	}
	if got := fake.Became()[0]; got.Name != "ssh" || got.Args[0] != "agent@192.168.122.10" {
		t.Errorf("became %v, want the command itself", got.Argv())
	}
}

// The remote command is re-split by the hypervisor's shell, so anything that
// shell would otherwise interpret has to survive intact.
func TestRemoteQuote(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"", "''"},
		{"virsh", "virsh"},
		{"/srv/agent-vm/vms/web-01/root.qcow2", "/srv/agent-vm/vms/web-01/root.qcow2"},
		{"--osinfo", "--osinfo"},
		{"detect=off,name=generic", "detect=off,name=generic"},
		{"kernel_args=root=/dev/vda1 console=ttyS0 rw", "'kernel_args=root=/dev/vda1 console=ttyS0 rw'"},
		{"$HOME", "'$HOME'"},
		{"~/state", "'~/state'"},
		{"a`id`b", "'a`id`b'"},
		{"a;rm -rf /", "'a;rm -rf /'"},
		{"it's", `'it'\''s'`},
		{"a\nb", "'a\nb'"},
		{"*", "'*'"},
	} {
		if got := remoteQuote(tc.in); got != tc.want {
			t.Errorf("remoteQuote(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// Render is what --dry-run prints for a plan built ahead of time, so it has to
// show the transport too.
func TestRemote_RenderShowsTheSSHInvocation(t *testing.T) {
	remote, _ := newRemoteFake(t)

	rendered := remote.Render(Command{Name: "virsh", Args: []string{"list"}})
	if !strings.HasPrefix(rendered, "ssh ") || !strings.Contains(rendered, "exec virsh list") {
		t.Errorf("Render = %q, want an ssh invocation carrying the command", rendered)
	}

	client := remote.Render(Command{Name: "gh", Args: []string{"auth", "status"}, Location: Client})
	if client != "gh auth status" {
		t.Errorf("Render of a client command = %q, want it unwrapped", client)
	}
}
