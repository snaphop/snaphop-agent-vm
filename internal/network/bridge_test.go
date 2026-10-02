package network

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/snaphop/snaphop-agent-vm/internal/hostexec"
)

const bridgeArgv = "ip -d -json link show type bridge"

// toolout reads output captured from a real tool. See test/toolout/README.md.
func toolout(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "test", "toolout", name))
	if err != nil {
		t.Fatalf("reading fixture %s: %v", name, err)
	}
	return string(b)
}

func bridgeFake(t *testing.T) *hostexec.Fake {
	t.Helper()
	return hostexec.NewFake().Respond(bridgeArgv, hostexec.FakeResponse{
		Stdout: toolout(t, "ip-d-json-link-show-type-bridge.txt"),
	})
}

func TestValidateBridge_AcceptsABridgeThatIsUp(t *testing.T) {
	// docker0 is up and has STP turned off, which is the shape a bridge should
	// have: nothing to report at all.
	warning, err := ValidateBridge(context.Background(), bridgeFake(t), "docker0")
	if err != nil {
		t.Fatalf("ValidateBridge(docker0) = %v, want nil", err)
	}
	if warning != nil {
		t.Errorf("ValidateBridge(docker0) warned about %s, and STP is off on it", warning)
	}
}

// A bridge running STP holds a guest's tap port in listening and learning for
// the forward delay each, so the guest cannot finish DHCP for twice that time
// and every boot on it is about 30 seconds longer. The bridge still works, so
// this is a warning the operator can act on and not a refusal.
func TestValidateBridge_WarnsAboutASpanningTreeForwardDelay(t *testing.T) {
	warning, err := ValidateBridge(context.Background(), bridgeFake(t), "br40")
	if err != nil {
		t.Fatalf("ValidateBridge(br40) = %v, want nil", err)
	}
	if warning == nil {
		t.Fatal("br40 runs STP with a 15s forward delay and drew no warning")
	}
	if warning.ForwardDelay != 15*time.Second {
		t.Errorf("ForwardDelay = %s, want 15s", warning.ForwardDelay)
	}
	if !strings.Contains(warning.Detail(), "30s") {
		t.Errorf("detail %q does not say how much boot time this costs", warning.Detail())
	}
	if !strings.Contains(warning.Remedy(), "ip link set br40 type bridge stp_state 0") {
		t.Errorf("remedy %q does not name the command that fixes it", warning.Remedy())
	}
}

func TestValidateBridge_RejectsABridgeThatIsDown(t *testing.T) {
	// virbr1 exists in the capture but has no carrier and reports operstate
	// DOWN. A guest attached to it would not reach anything.
	_, err := ValidateBridge(context.Background(), bridgeFake(t), "virbr1")

	var berr *BridgeError
	if !errors.As(err, &berr) {
		t.Fatalf("got %v, want *BridgeError", err)
	}
	if !strings.Contains(berr.Error(), "not up") {
		t.Errorf("error %q does not say the bridge is down", berr)
	}
}

func TestValidateBridge_RejectsAMissingBridgeAndListsWhatExists(t *testing.T) {
	_, err := ValidateBridge(context.Background(), bridgeFake(t), "br0")

	var berr *BridgeError
	if !errors.As(err, &berr) {
		t.Fatalf("got %v, want *BridgeError", err)
	}
	msg := berr.Error()
	if !strings.Contains(msg, "br40") {
		t.Errorf("error %q does not list the bridges that do exist", msg)
	}
	if !strings.Contains(msg, "ip link add") {
		t.Errorf("error %q does not tell the operator how to fix it", msg)
	}
}

func TestValidateBridge_NeverModifiesTheHost(t *testing.T) {
	// This package must not create, change, or bring up a host bridge.
	fake := bridgeFake(t)
	_, _ = ValidateBridge(context.Background(), fake, "br0")

	for _, call := range fake.Calls() {
		if call.Effect != hostexec.Read {
			t.Errorf("bridge validation ran a mutating command: %v", call.Argv())
		}
	}
}

func TestParseBridgeLinks_UnreadableOutputIsAnErrorNotAnEmptyList(t *testing.T) {
	// "no bridges" and "ip changed its output" must not look the same.
	for _, out := range []string{"", "not json at all", `[{"operstate":"UP"}]`} {
		_, err := parseBridgeLinks([]byte(out))
		var perr *hostexec.ParseError
		if !errors.As(err, &perr) {
			t.Errorf("parseBridgeLinks(%q) = %v, want *ParseError", out, err)
		}
	}
}

func TestParseBridgeLinks_EmptyArrayIsAHostWithNoBridges(t *testing.T) {
	links, err := parseBridgeLinks([]byte("[]\n"))
	if err != nil {
		t.Fatalf("parseBridgeLinks: %v", err)
	}
	if len(links) != 0 {
		t.Errorf("got %d links, want 0", len(links))
	}
}
