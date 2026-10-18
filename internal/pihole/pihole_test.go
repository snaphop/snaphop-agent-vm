package pihole

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
)

func TestPassword_FormattingDoesNotRevealIt(t *testing.T) {
	t.Parallel()
	password := Password{raw: "s3cret-value"}
	server := Server{BaseURL: "https://pi.hole", Password: password}
	for _, printed := range []string{
		fmt.Sprintf("%v", password),
		fmt.Sprintf("%+v", password),
		fmt.Sprintf("%#v", password),
		fmt.Sprintf("%q", password),
		fmt.Sprintf("%v", server),
		fmt.Sprintf("%#v", server),
	} {
		if strings.Contains(printed, "s3cret-value") {
			t.Errorf("formatting revealed the password: %s", printed)
		}
		if !strings.Contains(printed, "pihole-password") {
			t.Errorf("formatting = %s, want the redacted marker", printed)
		}
	}
}

func TestReadPassword_AcceptsOneLineAndATrailingNewline(t *testing.T) {
	t.Parallel()
	path := filepathJoin(t, "pihole-password")
	if err := os.WriteFile(path, []byte("s3cret-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := ReadPassword(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.raw != "s3cret-value" {
		t.Fatalf("password = %q", got.raw)
	}
}

func TestReadPassword_RejectsAFileOthersCanRead(t *testing.T) {
	t.Parallel()
	path := filepathJoin(t, "pihole-password")
	secret := "s3cret-value"
	if err := os.WriteFile(path, []byte(secret+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := ReadPassword(path)
	if err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Fatalf("ReadPassword = %v, want a chmod 600 remedy", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("the error contains the password: %v", err)
	}
}

func TestReadPassword_RejectsEmptyMultilineAndHugeFiles(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		mode os.FileMode
		body []byte
	}{
		{name: "empty", mode: 0o600, body: nil},
		{name: "newline only", mode: 0o600, body: []byte("\n")},
		{name: "two lines", mode: 0o600, body: []byte("one\ntwo\n")},
		{name: "nul", mode: 0o600, body: []byte("one\x00two")},
		{name: "too long", mode: 0o600, body: bytesRepeat(maxPasswordLength + 1)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := filepathJoin(t, "pihole-password")
			if err := os.WriteFile(path, tc.body, tc.mode); err != nil {
				t.Fatal(err)
			}
			_, err := ReadPassword(path)
			if err == nil {
				t.Fatal("ReadPassword succeeded")
			}
			if strings.Contains(err.Error(), "aaaa") {
				t.Fatalf("the error quotes the file: %v", err)
			}
		})
	}
}

func TestReadPassword_RejectsADirectory(t *testing.T) {
	t.Parallel()
	_, err := ReadPassword(t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "regular file") {
		t.Fatalf("ReadPassword = %v, want a regular-file error", err)
	}
}

func TestNormalizeAddress_StripsOneCIDRSuffix(t *testing.T) {
	t.Parallel()
	cases := map[string]string{
		"192.168.122.3":      "192.168.122.3",
		"192.168.122.3/24":   "192.168.122.3",
		" 2001:db8::1 ":      "2001:db8::1",
		"2001:db8:0:0::1/64": "2001:db8::1",
	}
	for raw, want := range cases {
		got, err := NormalizeAddress(raw)
		if err != nil {
			t.Fatalf("NormalizeAddress(%q) = %v", raw, err)
		}
		if got != want {
			t.Errorf("NormalizeAddress(%q) = %q, want %q", raw, got, want)
		}
	}
	if _, err := NormalizeAddress("not-an-ip"); err == nil {
		t.Fatal("NormalizeAddress accepted a name")
	}
}

func TestRecordName_UsesTheAgentDomainByDefault(t *testing.T) {
	t.Parallel()
	got, err := RecordName("agent-01", "")
	if err != nil {
		t.Fatal(err)
	}
	if got != "agent-01.agent" {
		t.Fatalf("RecordName = %q", got)
	}
	got, err = RecordName("agent-01", "lab.internal")
	if err != nil {
		t.Fatal(err)
	}
	if got != "agent-01.lab.internal" {
		t.Fatalf("RecordName = %q", got)
	}
	if _, err := RecordName("agent-01", "not a domain"); err == nil {
		t.Fatal("RecordName accepted a domain with a space")
	}
}

func TestEnsure_SkipsARecordThatAlreadyMatches(t *testing.T) {
	t.Parallel()
	api := newAPI(t, "s3cret-value")
	api.hosts = []string{"192.168.122.3 agent-01.agent", "10.0.0.1 other.agent"}
	server := api.server(t)
	defer server.Close()

	if err := (Server{BaseURL: server.URL, Password: Password{raw: api.password}}).Ensure(context.Background(), "agent-01.agent", "192.168.122.3"); err != nil {
		t.Fatal(err)
	}
	if len(api.snapshot().puts) != 0 || len(api.snapshot().deletes) != 0 {
		t.Fatalf("an exact record was rewritten: puts %v deletes %v", api.puts, api.deletes)
	}
	if api.snapshot().leak {
		t.Fatal("the password left POST /api/auth")
	}
}

func TestEnsure_ReplacesAnOldAddressAndKeepsOtherNames(t *testing.T) {
	t.Parallel()
	api := newAPI(t, "s3cret-value")
	api.hosts = []string{
		"10.0.0.1 other.agent",
		"10.0.0.9  agent-01.agent shared.agent",
	}
	server := api.server(t)
	defer server.Close()

	if err := (Server{BaseURL: server.URL, Password: Password{raw: api.password}}).Ensure(context.Background(), "agent-01.agent", "192.168.122.9/24"); err != nil {
		t.Fatal(err)
	}
	got := api.snapshot()
	if len(got.deletes) != 1 || !strings.Contains(got.deletes[0], "10.0.0.9%20%20agent-01.agent%20shared.agent") {
		t.Fatalf("delete URIs = %v, want the exact stored line", got.deletes)
	}
	if !containsAll(got.puts, "10.0.0.9 shared.agent", "192.168.122.9 agent-01.agent") {
		t.Fatalf("puts = %v", got.puts)
	}
	if !containsAll(got.hosts, "10.0.0.1 other.agent", "10.0.0.9 shared.agent", "192.168.122.9 agent-01.agent") {
		t.Fatalf("hosts = %v", got.hosts)
	}
	for _, host := range got.hosts {
		if strings.Contains(host, "agent-01.agent") && strings.Contains(host, "10.0.0.9") {
			t.Fatalf("the old address still has the name: %v", got.hosts)
		}
	}
	if got.leak {
		t.Fatal("the password left POST /api/auth")
	}
	for _, uri := range got.uris {
		if strings.Contains(uri, "dns%2Fhosts") {
			t.Fatalf("the element was encoded as one segment: %s", uri)
		}
	}
}

func TestEnsure_TreatsDeleteNotFoundAsGone(t *testing.T) {
	t.Parallel()
	api := newAPI(t, "s3cret-value")
	api.hosts = []string{"192.168.122.3 agent-01.agent"}
	api.deleteMissing = true
	server := api.server(t)
	defer server.Close()

	if err := (Server{BaseURL: server.URL, Password: Password{raw: api.password}}).Ensure(context.Background(), "agent-01.agent", "192.168.122.9"); err != nil {
		t.Fatal(err)
	}
	if !containsAll(api.snapshot().hosts, "192.168.122.9 agent-01.agent") {
		t.Fatalf("hosts = %v", api.hosts)
	}
}

func TestRemove_DropsOnlyTheNamedHost(t *testing.T) {
	t.Parallel()
	api := newAPI(t, "s3cret-value")
	api.hosts = []string{"10.0.0.1 other.agent", "192.168.122.3 agent-01.agent extra.agent"}
	server := api.server(t)
	defer server.Close()

	if err := (Server{BaseURL: server.URL, Password: Password{raw: api.password}}).Remove(context.Background(), "agent-01.agent"); err != nil {
		t.Fatal(err)
	}
	got := api.snapshot()
	if !containsAll(got.hosts, "10.0.0.1 other.agent", "192.168.122.3 extra.agent") {
		t.Fatalf("hosts = %v", got.hosts)
	}
	for _, host := range got.hosts {
		if strings.Contains(host, "agent-01.agent") {
			t.Fatalf("the name is still registered: %v", got.hosts)
		}
	}
}

func TestRemove_AMissingNameIsSuccess(t *testing.T) {
	t.Parallel()
	api := newAPI(t, "s3cret-value")
	server := api.server(t)
	defer server.Close()
	if err := (Server{BaseURL: server.URL, Password: Password{raw: api.password}}).Remove(context.Background(), "agent-01.agent"); err != nil {
		t.Fatal(err)
	}
}

func TestEnsure_RedactsThePasswordFromARefusal(t *testing.T) {
	t.Parallel()
	secret := "s3cret-value"
	api := newAPI(t, secret)
	api.rejectLogin = true
	server := api.server(t)
	defer server.Close()

	err := (Server{BaseURL: server.URL, Password: Password{raw: secret}}).Ensure(context.Background(), "agent-01.agent", "192.168.122.3")
	if err == nil {
		t.Fatal("Ensure succeeded")
	}
	if !strings.Contains(err.Error(), "HTTP 401") || !strings.Contains(err.Error(), "app_sudo") {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("the error contains the password: %v", err)
	}
}

func TestEnsure_DoesNotFollowARedirect(t *testing.T) {
	t.Parallel()
	secret := "s3cret-value"
	var hits []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, r.URL.Path)
		http.Redirect(w, r, "/stolen", http.StatusFound)
	}))
	defer server.Close()

	err := (Server{BaseURL: server.URL, Password: Password{raw: secret}}).Ensure(context.Background(), "agent-01.agent", "192.168.122.3")
	if err == nil || !strings.Contains(err.Error(), "Redirects are not followed") {
		t.Fatalf("Ensure = %v", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("the error contains the password: %v", err)
	}
	if len(hits) != 1 || hits[0] != "/api/auth" {
		t.Fatalf("requests = %v, want only the login", hits)
	}
}

func TestEnsure_InsecureTLSIsOptIn(t *testing.T) {
	t.Parallel()
	secret := "s3cret-value"
	api := newAPI(t, secret)
	server := api.tlsServer(t)
	defer server.Close()
	client := Server{BaseURL: server.URL, Password: Password{raw: secret}}

	err := client.Ensure(context.Background(), "agent-01.agent", "192.168.122.3")
	if err == nil {
		t.Fatal("Ensure verified a self-signed certificate")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("the error contains the password: %v", err)
	}

	client.Insecure = true
	if err := client.Ensure(context.Background(), "agent-01.agent", "192.168.122.3"); err != nil {
		t.Fatal(err)
	}
	if api.snapshot().leak {
		t.Fatal("the password left POST /api/auth")
	}
}

func TestEnsure_LogoutFailureDoesNotUndoTheRecord(t *testing.T) {
	t.Parallel()
	api := newAPI(t, "s3cret-value")
	api.logoutStatus = http.StatusInternalServerError
	server := api.server(t)
	defer server.Close()

	err := (Server{BaseURL: server.URL, Password: Password{raw: api.password}}).Ensure(context.Background(), "agent-01.agent", "192.168.122.3")
	var leak *SessionLeak
	if !errorAs(err, &leak) {
		t.Fatalf("Ensure = %v, want *SessionLeak", err)
	}
	if !containsAll(api.snapshot().hosts, "192.168.122.3 agent-01.agent") {
		t.Fatalf("hosts = %v", api.hosts)
	}
}

func TestEnsure_RejectsANameThatCouldChangeThePath(t *testing.T) {
	t.Parallel()
	api := newAPI(t, "s3cret-value")
	server := api.server(t)
	defer server.Close()
	err := (Server{BaseURL: server.URL, Password: Password{raw: api.password}}).Ensure(context.Background(), "agent 01.agent", "192.168.122.3")
	if err == nil {
		t.Fatal("Ensure accepted a name with a space")
	}
	if api.snapshot().hits != 0 {
		t.Fatal("a rejected name still called Pi-hole")
	}
}

type apiState struct {
	hosts   []string
	puts    []string
	deletes []string
	uris    []string
	leak    bool
	hits    int
}

type apiFake struct {
	password      string
	hosts         []string
	logoutStatus  int
	deleteMissing bool
	rejectLogin   bool

	mu      sync.Mutex
	puts    []string
	deletes []string
	uris    []string
	leak    bool
	hits    int
}

func newAPI(t *testing.T, password string) *apiFake {
	t.Helper()
	return &apiFake{password: password}
}

func (a *apiFake) server(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(a)
}

func (a *apiFake) tlsServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewTLSServer(a)
}

func (a *apiFake) snapshot() apiState {
	a.mu.Lock()
	defer a.mu.Unlock()
	return apiState{
		hosts:   append([]string(nil), a.hosts...),
		puts:    append([]string(nil), a.puts...),
		deletes: append([]string(nil), a.deletes...),
		uris:    append([]string(nil), a.uris...),
		leak:    a.leak,
		hits:    a.hits,
	}
}

func (a *apiFake) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.hits++
	a.uris = append(a.uris, r.RequestURI)
	body, _ := io.ReadAll(io.LimitReader(r.Body, maxBody))
	if r.URL.Path != "/api/auth" && strings.Contains(string(body), a.password) {
		a.leak = true
	}
	if strings.Contains(r.RequestURI, a.password) || headerHas(r.Header, a.password) {
		a.leak = true
	}

	switch {
	case r.URL.Path == "/api/auth" && r.Method == http.MethodPost:
		var payload struct {
			Password string `json:"password"`
		}
		_ = json.Unmarshal(body, &payload)
		if payload.Password != a.password || a.rejectLogin {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, "bad password "+payload.Password)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"session":{"valid":true,"sid":"sid-test"}}`)
	case r.URL.Path == "/api/auth" && r.Method == http.MethodDelete:
		if a.logoutStatus != 0 {
			w.WriteHeader(a.logoutStatus)
			_, _ = io.WriteString(w, "logout failed")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	case r.URL.Path == "/api/config/dns/hosts" && r.Method == http.MethodGet:
		if r.Header.Get("X-FTL-SID") == "" || r.Header.Get("sid") == "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		payload, err := json.Marshal(map[string]any{
			"config": map[string]any{"dns": map[string]any{"hosts": a.hosts}},
		})
		if err != nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write(payload)
	case strings.HasPrefix(r.URL.Path, "/api/config/dns/hosts/") && r.Method == http.MethodPut:
		item := strings.TrimPrefix(r.URL.Path, "/api/config/dns/hosts/")
		if !strings.Contains(r.RequestURI, "%20") {
			a.leak = true
		}
		a.puts = append(a.puts, item)
		a.hosts = append(a.hosts, item)
		w.WriteHeader(http.StatusCreated)
	case strings.HasPrefix(r.URL.Path, "/api/config/dns/hosts/") && r.Method == http.MethodDelete:
		item := strings.TrimPrefix(r.URL.Path, "/api/config/dns/hosts/")
		a.deletes = append(a.deletes, r.RequestURI)
		if a.deleteMissing {
			http.NotFound(w, r)
			return
		}
		next := make([]string, 0, len(a.hosts))
		found := false
		for _, line := range a.hosts {
			if line == item {
				found = true
				continue
			}
			next = append(next, line)
		}
		a.hosts = next
		if !found {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.NotFound(w, r)
	}
}

func headerHas(header http.Header, secret string) bool {
	for key, values := range header {
		if strings.Contains(key, secret) {
			return true
		}
		for _, value := range values {
			if strings.Contains(value, secret) {
				return true
			}
		}
	}
	return false
}

func containsAll(got []string, want ...string) bool {
	for _, item := range want {
		found := false
		for _, candidate := range got {
			if candidate == item {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func filepathJoin(t *testing.T, name string) string {
	t.Helper()
	return t.TempDir() + "/" + name
}

func bytesRepeat(n int) []byte {
	return []byte(strings.Repeat("a", n))
}

func errorAs(err error, target any) bool {
	return asSession(err, target)
}

func asSession(err error, target any) bool {
	leak, ok := target.(**SessionLeak)
	if !ok {
		return false
	}
	var found *SessionLeak
	if !errorsAs(err, &found) {
		return false
	}
	*leak = found
	return true
}

func errorsAs(err error, target **SessionLeak) bool {
	for err != nil {
		if leak, ok := err.(*SessionLeak); ok {
			*target = leak
			return true
		}
		unwrap, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = unwrap.Unwrap()
	}
	return false
}
