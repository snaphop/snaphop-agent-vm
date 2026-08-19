package domain

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"git.snaphop.xyz/snaphop/snaphop-agent-vm/internal/hostexec"
)

const uri = "qemu:///system"

// toolout reads a fixture captured from the real tool. Parsers are tested
// against these rather than against invented output (AGENTS.md §7).
func toolout(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join("..", "..", "test", "toolout", name)
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading the captured tool output %s: %v", name, err)
	}
	return string(contents)
}

func manager(fake *hostexec.Fake) *Manager { return New(fake, uri) }

func TestParseDomifaddr_ReadsARealVirshTable(t *testing.T) {
	got, err := parseDomifaddr([]byte(toolout(t, "virsh-domifaddr.txt")))
	if err != nil {
		t.Fatalf("parseDomifaddr: %v", err)
	}
	want := []Interface{{
		Name: "testnet0", MAC: "aa:bb:cc:dd:ee:ff",
		Protocol: "ipv4", Address: "192.168.122.3", Prefix: 24,
	}}
	if len(got) != len(want) || got[0] != want[0] {
		t.Errorf("parseDomifaddr = %+v, want %+v", got, want)
	}
}

func TestParseDomifaddr_AGuestWithNoAddressYetIsNotAnError(t *testing.T) {
	// virsh prints the header and nothing under it until the guest has an
	// address, which is the normal state for the first seconds of a boot.
	header := " Name       MAC address         Protocol   Address\n" +
		"-------------------------------------------------------------\n\n"

	got, err := parseDomifaddr([]byte(header))
	if err != nil {
		t.Fatalf("parseDomifaddr: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("parseDomifaddr = %+v, want no interfaces", got)
	}
}

func TestParseDomifaddr_UnreadableOutputIsAnErrorNotAnEmptyResult(t *testing.T) {
	if _, err := parseDomifaddr([]byte("error: failed to get domain 'agent-01'\n")); err == nil {
		t.Fatal("want an error: reporting 'no addresses' here would look like a booting guest")
	}
}

func TestIPv4Address_SkipsTheGuestsLoopbackInterface(t *testing.T) {
	// The guest agent reports every interface, and lists lo with 127.0.0.1
	// first. Returning it sends ssh to the host's own sshd, where it either
	// hangs on an unrelated host or is reset — a failure that looks like the
	// guest refusing logins rather than like the wrong address.
	fake := hostexec.NewFake()
	fake.Respond("virsh --connect "+uri+" domifaddr agent-01 --source agent", hostexec.FakeResponse{
		Stdout: toolout(t, "virsh-domifaddr-source-agent.txt"),
	})

	address, err := manager(fake).IPv4Address(context.Background(), "agent-01")
	if err != nil {
		t.Fatalf("IPv4Address: %v", err)
	}
	if address != "192.168.171.178" {
		t.Errorf("address = %q, want the guest's own address 192.168.171.178", address)
	}
}

func TestAddresses_FallsBackFromTheGuestAgentToTheDHCPLease(t *testing.T) {
	fake := hostexec.NewFake()
	// A guest whose agent is not up yet: virsh fails the agent query outright.
	fake.Respond("virsh --connect "+uri+" domifaddr agent-01 --source agent", hostexec.FakeResponse{
		ExitCode: 1,
		Stderr:   "error: Guest agent is not responding: QEMU guest agent is not connected",
	})
	fake.Respond("virsh --connect "+uri+" domifaddr agent-01 --source lease", hostexec.FakeResponse{
		Stdout: toolout(t, "virsh-domifaddr.txt"),
	})

	address, err := manager(fake).IPv4Address(context.Background(), "agent-01")
	if err != nil {
		t.Fatalf("IPv4Address: %v", err)
	}
	if address != "192.168.122.3" {
		t.Errorf("address = %q, want the leased address", address)
	}
}

func TestAddresses_ReportsFailureWhenNoSourceAnswers(t *testing.T) {
	fake := hostexec.NewFake()
	fake.Default = hostexec.FakeResponse{ExitCode: 1, Stderr: "error: failed to get domain 'agent-01'"}

	if _, err := manager(fake).Addresses(context.Background(), "agent-01"); err == nil {
		t.Fatal("want an error when every address source failed")
	}
}

func TestAddresses_AGuestThatSimplyHasNoAddressIsNotAFailure(t *testing.T) {
	fake := hostexec.NewFake()
	fake.Default = hostexec.FakeResponse{
		Stdout: " Name       MAC address         Protocol   Address\n" +
			"-------------------------------------------------------------\n\n",
	}

	address, err := manager(fake).IPv4Address(context.Background(), "agent-01")
	if err != nil {
		t.Fatalf("IPv4Address: %v", err)
	}
	if address != "" {
		t.Errorf("address = %q, want none yet", address)
	}
}

func TestMAC_ReadsTheFirstInterfaceFromARealDomiflistTable(t *testing.T) {
	fake := hostexec.NewFake()
	fake.Respond("virsh --connect "+uri+" domiflist agent-01", hostexec.FakeResponse{
		Stdout: toolout(t, "virsh-domiflist.txt"),
	})

	mac, err := manager(fake).MAC(context.Background(), "agent-01")
	if err != nil {
		t.Fatalf("MAC: %v", err)
	}
	if mac != "aa:bb:cc:dd:ee:ff" {
		t.Errorf("MAC = %q, want the interface's MAC address", mac)
	}
}

// virsh prints a blank line after the last name, which every caller of
// ListNames would otherwise see as a domain named "". The fixture is what the
// real tool emits, so the trailing newline being there is a fact rather than
// something a test author remembered.
func TestListNames_DropsTheBlankLineVirshPrints(t *testing.T) {
	fake := hostexec.NewFake()
	fake.Respond("virsh --connect "+uri+" list --all --name", hostexec.FakeResponse{
		Stdout: toolout(t, "virsh-list-all-name.txt"),
	})

	names, err := manager(fake).ListNames(context.Background())
	if err != nil {
		t.Fatalf("ListNames: %v", err)
	}
	if len(names) != 1 || names[0] != "test" {
		t.Errorf("ListNames = %q, want exactly [test]", names)
	}
}

func TestState_ReadsTheStateOfADefinedDomain(t *testing.T) {
	fake := hostexec.NewFake()
	fake.Respond("virsh --connect "+uri+" list --all --name", hostexec.FakeResponse{Stdout: "agent-01\n\n"})
	fake.Respond("virsh --connect "+uri+" domstate agent-01", hostexec.FakeResponse{
		Stdout: toolout(t, "virsh-domstate.txt"),
	})

	state, err := manager(fake).State(context.Background(), "agent-01")
	if err != nil {
		t.Fatalf("State: %v", err)
	}
	if !state.IsRunning() {
		t.Errorf("state = %q, want running", state)
	}
}

func TestState_ADomainLibvirtNoLongerHasIsMissing(t *testing.T) {
	fake := hostexec.NewFake()
	fake.Respond("virsh --connect "+uri+" list --all --name", hostexec.FakeResponse{Stdout: "other-vm\n"})

	state, err := manager(fake).State(context.Background(), "agent-01")
	if err != nil {
		t.Fatalf("State: %v", err)
	}
	if state != StateMissing {
		t.Errorf("state = %q, want %q", state, StateMissing)
	}
	// A VM that vanished from libvirt is reported, not repaired: nothing here
	// may define or delete anything on the operator's behalf.
	for _, argv := range fake.Argvs() {
		if strings.Contains(argv, "define") || strings.Contains(argv, "undefine") {
			t.Errorf("reading a state must not change it: %v", argv)
		}
	}
}

func TestUndefine_NeverRemovesStorage(t *testing.T) {
	fake := hostexec.NewFake()

	if err := manager(fake).Undefine(context.Background(), "agent-01"); err != nil {
		t.Fatalf("Undefine: %v", err)
	}
	if !fake.Ran("virsh --connect " + uri + " undefine agent-01") {
		t.Errorf("unexpected invocation:\n%s", fake)
	}
	// Deleting files is this tool's job, and only inside its own state
	// directory (SECURITY.md).
	for _, argv := range fake.Argvs() {
		if strings.Contains(argv, "--remove-all-storage") {
			t.Errorf("virsh must never be given --remove-all-storage: %v", argv)
		}
	}
}

func TestShutdown_RequestsAGracefulStopAndNeverForcesOne(t *testing.T) {
	fake := hostexec.NewFake()

	if err := manager(fake).Shutdown(context.Background(), "agent-01"); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if !fake.Ran("virsh --connect " + uri + " shutdown agent-01") {
		t.Errorf("unexpected invocation:\n%s", fake)
	}
	for _, argv := range fake.Argvs() {
		if strings.Contains(argv, " destroy ") {
			t.Errorf("a graceful stop must never escalate on its own: %v", argv)
		}
	}
}

func TestCreate_RunsVirtInstallAndReturnsTheArgvItRan(t *testing.T) {
	fake := hostexec.NewFake()

	argv, err := manager(fake).Create(context.Background(), natOptions())
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if argv[0] != "virt-install" {
		t.Errorf("recorded argv starts with %q, want virt-install", argv[0])
	}
	// The argv is recorded in vm.json, so it has to be the vector that ran.
	if got := strings.Join(fake.Argvs(), "\n"); got != strings.Join(argv, " ") {
		t.Errorf("recorded argv does not match what ran:\n%s\n%s", got, strings.Join(argv, " "))
	}
}

func TestCreate_RejectsBadOptionsBeforeTouchingTheHost(t *testing.T) {
	fake := hostexec.NewFake()
	opts := natOptions()
	opts.Name = "Agent VM"

	if _, err := manager(fake).Create(context.Background(), opts); err == nil {
		t.Fatal("want a refusal for an invalid VM name")
	}
	if len(fake.Calls()) != 0 {
		t.Errorf("nothing may run when the options are invalid:\n%s", fake)
	}
}

func TestWaitForAddress_ReturnsTheAddressAsSoonAsItAppears(t *testing.T) {
	fake := hostexec.NewFake()
	fake.RespondPrefix("virsh --connect "+uri+" domifaddr", hostexec.FakeResponse{
		Stdout: toolout(t, "virsh-domifaddr.txt"),
	})

	address, err := manager(fake).WaitForAddress(context.Background(), "agent-01", time.Second)
	if err != nil {
		t.Fatalf("WaitForAddress: %v", err)
	}
	if address != "192.168.122.3" {
		t.Errorf("address = %q", address)
	}
}

func TestWaitForAddress_TimesOutWithAnActionableError(t *testing.T) {
	fake := hostexec.NewFake()
	fake.RespondPrefix("virsh --connect "+uri+" domifaddr", hostexec.FakeResponse{
		Stdout: " Name       MAC address         Protocol   Address\n" +
			"-------------------------------------------------------------\n\n",
	})

	// A zero timeout still checks once: "wait for nothing" is not "skip".
	_, err := manager(fake).WaitForAddress(context.Background(), "agent-01", 0)
	var timeout *TimeoutError
	if !errors.As(err, &timeout) {
		t.Fatalf("err = %v, want a *TimeoutError", err)
	}
	if !strings.Contains(timeout.Error(), "console log") {
		t.Errorf("a boot timeout should point at the evidence: %v", timeout)
	}
}

func TestWaitForShutdown_ReturnsWhenTheGuestStops(t *testing.T) {
	fake := hostexec.NewFake()
	fake.Respond("virsh --connect "+uri+" list --all --name", hostexec.FakeResponse{Stdout: "agent-01\n"})
	fake.Respond("virsh --connect "+uri+" domstate agent-01", hostexec.FakeResponse{Stdout: "shut off\n"})

	if err := manager(fake).WaitForShutdown(context.Background(), "agent-01", time.Second); err != nil {
		t.Fatalf("WaitForShutdown: %v", err)
	}
}

func TestWaitForShutdown_TimesOutWhenTheGuestIgnoresTheRequest(t *testing.T) {
	fake := hostexec.NewFake()
	fake.Respond("virsh --connect "+uri+" list --all --name", hostexec.FakeResponse{Stdout: "agent-01\n"})
	fake.Respond("virsh --connect "+uri+" domstate agent-01", hostexec.FakeResponse{Stdout: "running\n"})

	err := manager(fake).WaitForShutdown(context.Background(), "agent-01", 0)
	var timeout *TimeoutError
	if !errors.As(err, &timeout) {
		t.Fatalf("err = %v, want a *TimeoutError", err)
	}
	if !strings.Contains(timeout.Error(), "--force") {
		t.Errorf("the error should name the operator's next move: %v", timeout)
	}
}

func TestConsoleCommand_IsTheVirshInvocationToExec(t *testing.T) {
	got := manager(hostexec.NewFake()).ConsoleCommand("agent-01")
	if want := "virsh --connect " + uri + " console agent-01"; got.String() != want {
		t.Errorf("ConsoleCommand = %q, want %q", got.String(), want)
	}
}
