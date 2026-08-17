package network

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.snaphop.xyz/snaphop/snaphop-agent-vm/internal/hostexec"
)

const bridgeArgv = "ip -json link show type bridge"

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
		Stdout: toolout(t, "ip-json-link-show-type-bridge.txt"),
	})
}

func TestValidateBridge_AcceptsABridgeThatIsUp(t *testing.T) {
	if err := ValidateBridge(context.Background(), bridgeFake(t), "br40"); err != nil {
		t.Fatalf("ValidateBridge(br40) = %v, want nil", err)
	}
}

func TestValidateBridge_RejectsABridgeThatIsDown(t *testing.T) {
	// docker0 exists in the capture but has no carrier and reports operstate
	// DOWN. A guest attached to it would not reach anything.
	err := ValidateBridge(context.Background(), bridgeFake(t), "docker0")

	var berr *BridgeError
	if !errors.As(err, &berr) {
		t.Fatalf("got %v, want *BridgeError", err)
	}
	if !strings.Contains(berr.Error(), "not up") {
		t.Errorf("error %q does not say the bridge is down", berr)
	}
}

func TestValidateBridge_RejectsAMissingBridgeAndListsWhatExists(t *testing.T) {
	err := ValidateBridge(context.Background(), bridgeFake(t), "br0")

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
	_ = ValidateBridge(context.Background(), fake, "br0")

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
