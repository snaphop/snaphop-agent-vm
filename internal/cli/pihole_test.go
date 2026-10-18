package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/snaphop/snaphop-agent-vm/internal/hostexec"
	"github.com/snaphop/snaphop-agent-vm/internal/state"
)

func TestCreate_RegistersTheGuestOnPiHole(t *testing.T) {
	t.Parallel()
	secret := "s3cret-pihole-password"
	api := newPiHoleAPI(secret)
	server := httptest.NewServer(api)
	defer server.Close()

	stateDir, keyPath := createEnv(t)
	passPath := writePiHolePassword(t, secret)
	fake := createHost(t)
	code, stdout, stderr := cliRun(t, fake, stateDir, createArgs(keyPath,
		"--no-update", "--pihole", "--pihole-url", server.URL, "--pihole-password-file", passPath)...)
	if code != ExitOK {
		t.Fatalf("exit code = %d\nstdout:\n%s\nstderr:\n%s", code, stdout, stderr)
	}

	vm := loadVM(t, stateDir, "agent-01")
	if vm.SchemaVersion != state.VMSchemaVersion {
		t.Errorf("schemaVersion = %d", vm.SchemaVersion)
	}
	if vm.PiHole == nil {
		t.Fatal("vm.json has no pihole record")
	}
	if vm.PiHole.Name != "agent-01.agent" || vm.PiHole.Address != "192.168.122.3" || vm.PiHole.URL != server.URL || vm.PiHole.PasswordFile != passPath {
		t.Errorf("pihole = %+v", vm.PiHole)
	}
	if vm.PiHole.Insecure {
		t.Error("insecure was recorded without --pihole-insecure")
	}
	assertNoSecret(t, secret, stdout, stderr, readVMJSON(t, stateDir, "agent-01"), strings.Join(fake.Argvs(), "\n"))
	if !api.hasHost("192.168.122.3 agent-01.agent") {
		t.Fatalf("hosts = %v", api.hosts())
	}
	if !strings.Contains(stdout, "only this host can reach") {
		t.Errorf("NAT create did not say who can reach the name:\n%s", stdout)
	}
	if !strings.Contains(stdout, "agent-vm ssh agent-01") {
		t.Errorf("ssh line missing:\n%s", stdout)
	}
}

func TestCreate_PiHoleOnABridgeSaysOtherMachinesCanOpenTheName(t *testing.T) {
	t.Parallel()
	secret := "s3cret-pihole-password"
	api := newPiHoleAPI(secret)
	server := httptest.NewServer(api)
	defer server.Close()

	stateDir, keyPath := createEnv(t)
	passPath := writePiHolePassword(t, secret)
	fake := createHost(t)
	fake.Respond("ip -d -json link show type bridge", hostexec.FakeResponse{
		Stdout: readToolout(t, "ip-d-json-link-show-type-bridge.txt"),
	})
	code, stdout, stderr := cliRun(t, fake, stateDir, createArgs(keyPath,
		"--no-update", "--network", "bridge", "--bridge", "br40",
		"--pihole", "--pihole-url", server.URL, "--pihole-password-file", passPath, "--pihole-insecure")...)
	if code != ExitOK {
		t.Fatalf("exit code = %d\n%s\n%s", code, stdout, stderr)
	}
	if !strings.Contains(stdout, "Other machines that use this Pi-hole can open agent-01.agent.") {
		t.Errorf("bridge create did not name who can open the record:\n%s", stdout)
	}
	vm := loadVM(t, stateDir, "agent-01")
	if vm.PiHole == nil || !vm.PiHole.Insecure {
		t.Fatalf("pihole = %+v, want insecure recorded", vm.PiHole)
	}
}

func TestCreate_PiHoleAPIFailureKeepsTheVMAndThePassword(t *testing.T) {
	t.Parallel()
	secret := "s3cret-pihole-password"
	api := newPiHoleAPI(secret)
	api.failAuth.Store(true)
	server := httptest.NewServer(api)
	defer server.Close()

	stateDir, keyPath := createEnv(t)
	passPath := writePiHolePassword(t, secret)
	code, _, stderr := cliRun(t, createHost(t), stateDir, createArgs(keyPath,
		"--pihole", "--pihole-url", server.URL, "--pihole-password-file", passPath)...)
	if code == ExitOK {
		t.Fatal("create succeeded")
	}
	if !vmDirExists(t, stateDir, "agent-01") {
		t.Fatal("the VM was removed after a Pi-hole failure")
	}
	if strings.Contains(readVMJSON(t, stateDir, "agent-01"), `"pihole"`) {
		t.Fatal("vm.json recorded a registration that did not succeed")
	}
	assertNoSecret(t, secret, stderr)
	if !strings.Contains(stderr, "the guest update did not run") {
		t.Errorf("the error should name the steps that did not run:\n%s", stderr)
	}
}

func TestCreate_PiHoleLogoutFailureStillRecordsTheName(t *testing.T) {
	t.Parallel()
	secret := "s3cret-pihole-password"
	api := newPiHoleAPI(secret)
	api.logoutStatus = http.StatusInternalServerError
	server := httptest.NewServer(api)
	defer server.Close()

	stateDir, keyPath := createEnv(t)
	passPath := writePiHolePassword(t, secret)
	code, _, stderr := cliRun(t, createHost(t), stateDir, createArgs(keyPath,
		"--no-update", "--pihole", "--pihole-url", server.URL, "--pihole-password-file", passPath)...)
	if code != ExitOK {
		t.Fatalf("exit code = %d\n%s", code, stderr)
	}
	if !strings.Contains(stderr, "logging out") {
		t.Errorf("stderr did not warn about the session:\n%s", stderr)
	}
	vm := loadVM(t, stateDir, "agent-01")
	if vm.PiHole == nil || vm.PiHole.Name != "agent-01.agent" {
		t.Fatalf("pihole = %+v", vm.PiHole)
	}
	assertNoSecret(t, secret, stderr)
}

func TestCreate_RejectsPiHoleBeforeAnythingIsCreated(t *testing.T) {
	t.Parallel()
	secret := "s3cret-pihole-password"
	passPath := writePiHolePassword(t, secret)
	readable := filepath.Join(t.TempDir(), "open-password")
	if err := os.WriteFile(readable, []byte(secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(readable, 0o644); err != nil {
		t.Fatal(err)
	}
	const baseURL = "http://127.0.0.1:9"

	cases := []struct {
		name string
		args []string
		want string
	}{
		{name: "no url", args: []string{"--pihole", "--pihole-password-file", passPath}, want: "pi-hole url"},
		{name: "flags without pihole", args: []string{"--pihole-domain", "lab"}, want: "--pihole"},
		{name: "wait disabled", args: []string{"--pihole", "--pihole-url", baseURL, "--pihole-password-file", passPath, "--wait-for-ssh", "0"}, want: "--wait-for-ssh"},
		{name: "world readable", args: []string{"--pihole", "--pihole-url", baseURL, "--pihole-password-file", readable}, want: "chmod 600"},
		{name: "bad url", args: []string{"--pihole", "--pihole-url", "ftp://pi.hole", "--pihole-password-file", passPath}, want: "pi-hole url"},
		{name: "leading dot", args: []string{"--pihole", "--pihole-url", baseURL, "--pihole-password-file", passPath, "--pihole-domain", ".agent"}, want: "Use agent, not .agent."},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir, key := createEnv(t)
			fake := createHost(t)
			code, _, stderr := cliRun(t, fake, dir, createArgs(key, tc.args...)...)
			if code != ExitUsage {
				t.Fatalf("exit code = %d, want %d\n%s", code, ExitUsage, stderr)
			}
			if !strings.Contains(stderr, tc.want) {
				t.Errorf("stderr missing %q:\n%s", tc.want, stderr)
			}
			if strings.Contains(stderr, secret) {
				t.Errorf("stderr contains the password:\n%s", stderr)
			}
			for _, argv := range fake.Argvs() {
				if strings.Contains(argv, "virt-install") {
					t.Errorf("virt-install ran: %s", argv)
				}
			}
			if vmDirExists(t, dir, "agent-01") {
				t.Fatal("a rejected create left a VM directory")
			}
		})
	}
}

func TestCreate_DryRunPrintsThePiHolePlanAndDoesNotCallIt(t *testing.T) {
	t.Parallel()
	secret := "s3cret-pihole-password"
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		calls.Add(1)
	}))
	defer server.Close()

	stateDir, keyPath := createEnv(t)
	passPath := writePiHolePassword(t, secret)
	code, stdout, stderr := cliRun(t, createHost(t), stateDir, append([]string{"--dry-run"}, createArgs(keyPath,
		"--pihole", "--pihole-url", server.URL, "--pihole-password-file", passPath)...)...)
	if code != ExitOK {
		t.Fatalf("exit code = %d\n%s\n%s", code, stdout, stderr)
	}
	for _, want := range []string{"agent-01.agent", server.URL, passPath} {
		if !strings.Contains(stdout, want) {
			t.Errorf("plan missing %q:\n%s", want, stdout)
		}
	}
	assertNoSecret(t, secret, stdout, stderr)
	if calls.Load() != 0 {
		t.Fatal("dry-run called Pi-hole")
	}
	if vmDirExists(t, stateDir, "agent-01") {
		t.Fatal("dry-run created a VM directory")
	}
}

func TestInfoAndList_ShowThePiHoleRecord(t *testing.T) {
	t.Parallel()
	stateDir := createPiHoleVM(t, "s3cret-pihole-password")

	code, stdout, stderr := cliRun(t, runningHost(t, "agent-01"), stateDir, "info", "agent-01")
	if code != ExitOK {
		t.Fatalf("info exit = %d\n%s", code, stderr)
	}
	if !strings.Contains(stdout, "agent-01.agent") || !strings.Contains(stdout, "192.168.122.3") {
		t.Errorf("info missing the DNS row:\n%s", stdout)
	}

	code, stdout, stderr = cliRun(t, runningHost(t, "agent-01"), stateDir, "list")
	if code != ExitOK {
		t.Fatalf("list exit = %d\n%s", code, stderr)
	}
	if !strings.Contains(stdout, "+pihole") {
		t.Errorf("list network missing +pihole:\n%s", stdout)
	}
}

func TestDestroy_RemovesThePiHoleRecordBeforeTheVM(t *testing.T) {
	t.Parallel()
	secret := "s3cret-pihole-password"
	api := newPiHoleAPI(secret)
	stateDir := createPiHoleVMOn(t, api, secret)

	code, stdout, stderr := cliRunStdin(t, ownedBy(runningHost(t, "agent-01"), stateDir, "agent-01"), stateDir, "",
		"--yes", "destroy", "agent-01", "--force")
	if code != ExitOK {
		t.Fatalf("exit code = %d\n%s\n%s", code, stdout, stderr)
	}
	if api.hasHost("192.168.122.3 agent-01.agent") {
		t.Fatalf("the name is still registered: %v", api.hosts())
	}
	if vmDirExists(t, stateDir, "agent-01") {
		t.Fatal("the VM directory remains")
	}
	assertNoSecret(t, secret, stdout, stderr)
}

func TestDestroy_KeepsTheVMWhenPiHoleRefuses(t *testing.T) {
	t.Parallel()
	secret := "s3cret-pihole-password"
	api := newPiHoleAPI(secret)
	stateDir := createPiHoleVMOn(t, api, secret)
	api.failAuth.Store(true)

	code, _, stderr := cliRunStdin(t, ownedBy(runningHost(t, "agent-01"), stateDir, "agent-01"), stateDir, "",
		"--yes", "destroy", "agent-01", "--force")
	if code == ExitOK {
		t.Fatal("destroy succeeded")
	}
	if !vmDirExists(t, stateDir, "agent-01") {
		t.Fatal("the VM was removed")
	}
	if !strings.Contains(readVMJSON(t, stateDir, "agent-01"), `"pihole"`) {
		t.Fatal("the pihole record was cleared after a failed removal")
	}
	assertNoSecret(t, secret, stderr)
}

func TestDestroy_KeepDiskStillRemovesTheDNSName(t *testing.T) {
	t.Parallel()
	secret := "s3cret-pihole-password"
	api := newPiHoleAPI(secret)
	stateDir := createPiHoleVMOn(t, api, secret)

	code, _, stderr := cliRunStdin(t, ownedBy(runningHost(t, "agent-01"), stateDir, "agent-01"), stateDir, "",
		"--yes", "destroy", "agent-01", "--force", "--keep-disk")
	if code != ExitOK {
		t.Fatalf("exit code = %d\n%s", code, stderr)
	}
	if api.hasHost("192.168.122.3 agent-01.agent") {
		t.Fatalf("hosts = %v", api.hosts())
	}
	vm := loadVM(t, stateDir, "agent-01")
	if vm.PiHole != nil {
		t.Fatalf("pihole record remains: %+v", vm.PiHole)
	}
}

func TestDestroy_DryRunDoesNotCallPiHole(t *testing.T) {
	t.Parallel()
	secret := "s3cret-pihole-password"
	api := newPiHoleAPI(secret)
	stateDir := createPiHoleVMOn(t, api, secret)
	before := api.hitCount()

	code, stdout, stderr := cliRunStdin(t, ownedBy(runningHost(t, "agent-01"), stateDir, "agent-01"), stateDir, "",
		"--dry-run", "--yes", "destroy", "agent-01", "--force")
	if code != ExitOK {
		t.Fatalf("exit code = %d\n%s\n%s", code, stdout, stderr)
	}
	if api.hitCount() != before {
		t.Fatalf("dry-run called Pi-hole (%d -> %d)", before, api.hitCount())
	}
	if !strings.Contains(stdout, "agent-01.agent") || !strings.Contains(stdout, "DELETE") {
		t.Errorf("plan missing the DELETE:\n%s", stdout)
	}
	assertNoSecret(t, secret, stdout, stderr)
	if !strings.Contains(readVMJSON(t, stateDir, "agent-01"), `"pihole"`) {
		t.Fatal("dry-run cleared the pihole record")
	}
}

func TestStart_RefreshesPiHoleWhenTheAddressChanges(t *testing.T) {
	t.Parallel()
	secret := "s3cret-pihole-password"
	api := newPiHoleAPI(secret)
	stateDir := createPiHoleVMOn(t, api, secret)

	fake := createHost(t)
	started := false
	fake.MatchFunc = func(c hostexec.Command) (hostexec.FakeResponse, bool) {
		argv := strings.Join(c.Argv(), " ")
		switch {
		case strings.Contains(argv, " domstate "):
			state := "shut off\n"
			if started {
				state = "running\n"
			}
			return hostexec.FakeResponse{Stdout: state}, true
		case strings.Contains(argv, " start "):
			started = true
			return hostexec.FakeResponse{}, true
		case strings.Contains(argv, " domifaddr "):
			return hostexec.FakeResponse{Stdout: domifaddrTable("192.168.122.9")}, true
		case strings.Contains(argv, " list --all --name"):
			return hostexec.FakeResponse{Stdout: "agent-01\n"}, true
		default:
			return hostexec.FakeResponse{}, false
		}
	}

	code, _, stderr := cliRun(t, fake, stateDir, "start", "agent-01")
	if code != ExitOK {
		t.Fatalf("exit code = %d\n%s", code, stderr)
	}
	vm := loadVM(t, stateDir, "agent-01")
	if vm.PiHole == nil || vm.PiHole.Address != "192.168.122.9" {
		t.Fatalf("address = %+v", vm.PiHole)
	}
	if api.hasHost("192.168.122.3 agent-01.agent") || !api.hasHost("192.168.122.9 agent-01.agent") {
		t.Fatalf("hosts = %v", api.hosts())
	}
	assertNoSecret(t, secret, stderr)
}

func TestStart_LeavesPiHoleAloneWhenTheAddressIsUnchanged(t *testing.T) {
	t.Parallel()
	secret := "s3cret-pihole-password"
	api := newPiHoleAPI(secret)
	stateDir := createPiHoleVMOn(t, api, secret)
	before := api.hitCount()
	vm := loadVM(t, stateDir, "agent-01")
	if err := os.Remove(vm.PiHole.PasswordFile); err != nil {
		t.Fatal(err)
	}

	code, _, stderr := cliRun(t, startHost(t, "192.168.122.3"), stateDir, "start", "agent-01")
	if code != ExitOK {
		t.Fatalf("exit code = %d\n%s", code, stderr)
	}
	if api.hitCount() != before {
		t.Fatalf("start called Pi-hole for an unchanged address (%d -> %d)", before, api.hitCount())
	}
}

func TestStart_DoesNotFailWhenTheNewAddressIsUnknown(t *testing.T) {
	t.Parallel()
	secret := "s3cret-pihole-password"
	api := newPiHoleAPI(secret)
	stateDir := createPiHoleVMOn(t, api, secret)
	before := api.hitCount()

	code, _, stderr := cliRun(t, startHost(t, ""), stateDir, "start", "agent-01")
	if code != ExitOK {
		t.Fatalf("exit code = %d\n%s", code, stderr)
	}
	if api.hitCount() != before {
		t.Fatal("start called Pi-hole without an address")
	}
	vm := loadVM(t, stateDir, "agent-01")
	if vm.PiHole.Address != "192.168.122.3" {
		t.Fatalf("address changed to %s", vm.PiHole.Address)
	}
}

func TestStart_PiHoleRefreshFailureLeavesTheOldAddress(t *testing.T) {
	t.Parallel()
	secret := "s3cret-pihole-password"
	api := newPiHoleAPI(secret)
	stateDir := createPiHoleVMOn(t, api, secret)
	api.failAuth.Store(true)

	code, _, stderr := cliRun(t, startHost(t, "192.168.122.9"), stateDir, "start", "agent-01")
	if code == ExitOK {
		t.Fatal("start succeeded")
	}
	if !strings.Contains(stderr, "is running") || !strings.Contains(stderr, "192.168.122.3") || !strings.Contains(stderr, "stop") {
		t.Errorf("stderr = %s", stderr)
	}
	vm := loadVM(t, stateDir, "agent-01")
	if vm.PiHole.Address != "192.168.122.3" {
		t.Fatalf("address = %s", vm.PiHole.Address)
	}
	assertNoSecret(t, secret, stderr)
}

func TestDestroyPrompt_NamesTheDNSRecord(t *testing.T) {
	t.Parallel()
	vm := &state.VM{
		Name:   "agent-01",
		Paths:  state.VMPaths{Dir: "/tmp/agent-01"},
		PiHole: &state.VMPiHole{Name: "agent-01.agent"},
	}
	if !strings.Contains(destroyPrompt(vm, false, false, false), "agent-01.agent") {
		t.Fatalf("prompt = %s", destroyPrompt(vm, false, false, false))
	}
}

func createPiHoleVM(t *testing.T, secret string) string {
	t.Helper()
	api := newPiHoleAPI(secret)
	return createPiHoleVMOn(t, api, secret)
}

func createPiHoleVMOn(t *testing.T, api *piHoleAPI, secret string) string {
	t.Helper()
	server := httptest.NewServer(api)
	t.Cleanup(server.Close)
	stateDir, keyPath := createEnv(t)
	passPath := writePiHolePassword(t, secret)
	code, stdout, stderr := cliRun(t, createHost(t), stateDir, createArgs(keyPath,
		"--no-update", "--pihole", "--pihole-url", server.URL, "--pihole-password-file", passPath)...)
	if code != ExitOK {
		t.Fatalf("create failed with %d\n%s\n%s", code, stdout, stderr)
	}
	return stateDir
}

func writePiHolePassword(t *testing.T, secret string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "pihole-password")
	if err := os.WriteFile(path, []byte(secret+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func readVMJSON(t *testing.T, stateDir, name string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(stateDir, "vms", name, state.VMRecordFile))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func assertNoSecret(t *testing.T, secret string, parts ...string) {
	t.Helper()
	for _, part := range parts {
		if strings.Contains(part, secret) {
			t.Errorf("output contains the Pi-hole password:\n%s", part)
		}
	}
}

func startHost(t *testing.T, address string) *hostexec.Fake {
	t.Helper()
	fake := createHost(t)
	started := false
	table := " Name       MAC address         Protocol   Address\n-------------------------------------------------------------\n"
	if address != "" {
		table = domifaddrTable(address)
	}
	fake.MatchFunc = func(c hostexec.Command) (hostexec.FakeResponse, bool) {
		argv := strings.Join(c.Argv(), " ")
		switch {
		case strings.Contains(argv, " domstate "):
			state := "shut off\n"
			if started {
				state = "running\n"
			}
			return hostexec.FakeResponse{Stdout: state}, true
		case strings.Contains(argv, " start "):
			started = true
			return hostexec.FakeResponse{}, true
		case strings.Contains(argv, " domifaddr "):
			return hostexec.FakeResponse{Stdout: table}, true
		case strings.Contains(argv, " list --all --name"):
			return hostexec.FakeResponse{Stdout: "agent-01\n"}, true
		default:
			return hostexec.FakeResponse{}, false
		}
	}
	return fake
}

func domifaddrTable(address string) string {
	return " Name       MAC address         Protocol   Address\n" +
		"-------------------------------------------------------------\n" +
		" testnet0   aa:bb:cc:dd:ee:ff   ipv4       " + address + "/24\n"
}

type piHoleAPI struct {
	password     string
	logoutStatus int
	failAuth     atomic.Bool

	mu    sync.Mutex
	lines []string
	hits  int
}

func newPiHoleAPI(password string) *piHoleAPI {
	return &piHoleAPI{password: password}
}

func (a *piHoleAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.hits++
	body, _ := io.ReadAll(io.LimitReader(r.Body, 4096))
	switch {
	case r.URL.Path == "/api/auth" && r.Method == http.MethodPost:
		var payload struct {
			Password string `json:"password"`
		}
		_ = json.Unmarshal(body, &payload)
		if a.failAuth.Load() || payload.Password != a.password {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, "bad password "+payload.Password)
			return
		}
		_, _ = io.WriteString(w, `{"session":{"valid":true,"sid":"sid-test"}}`)
	case r.URL.Path == "/api/auth" && r.Method == http.MethodDelete:
		if a.logoutStatus != 0 {
			w.WriteHeader(a.logoutStatus)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case r.URL.Path == "/api/config/dns/hosts" && r.Method == http.MethodGet:
		payload, _ := json.Marshal(map[string]any{
			"config": map[string]any{"dns": map[string]any{"hosts": a.lines}},
		})
		_, _ = w.Write(payload)
	case strings.HasPrefix(r.URL.Path, "/api/config/dns/hosts/") && r.Method == http.MethodPut:
		a.lines = append(a.lines, strings.TrimPrefix(r.URL.Path, "/api/config/dns/hosts/"))
		w.WriteHeader(http.StatusCreated)
	case strings.HasPrefix(r.URL.Path, "/api/config/dns/hosts/") && r.Method == http.MethodDelete:
		item := strings.TrimPrefix(r.URL.Path, "/api/config/dns/hosts/")
		next := make([]string, 0, len(a.lines))
		for _, line := range a.lines {
			if line != item {
				next = append(next, line)
			}
		}
		a.lines = next
		w.WriteHeader(http.StatusNoContent)
	default:
		http.NotFound(w, r)
	}
}

func (a *piHoleAPI) hosts() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.lines...)
}

func (a *piHoleAPI) hasHost(line string) bool {
	for _, got := range a.hosts() {
		if got == line {
			return true
		}
	}
	return false
}

func (a *piHoleAPI) hitCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.hits
}
