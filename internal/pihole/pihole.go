// Package pihole registers a guest's address on a Pi-hole v6 DNS server.
//
// The password is a credential. It is read from a file the operator names and
// sent only in the body of POST /api/auth, from the client. It is never logged,
// never placed in an argument vector, and never written to a base image, a
// cloud-init seed, or vm.json (ADR-0021, SECURITY.md). Pi-hole has no local
// CLI this tool can drive on another machine, so the call is HTTP rather than
// a host tool (ADR-0009).
package pihole

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/snaphop/snaphop-agent-vm/internal/config"
)

const (
	// maxPasswordLength is how large a password file may be. A longer file is
	// not a password, and it is not sent.
	maxPasswordLength = 4096

	// maxBody is how much of a Pi-hole response is kept. The error an operator
	// sees is a short excerpt of that, never the whole body.
	maxBody = 4096

	// maxExcerpt is the longest Pi-hole response fragment an error may quote.
	maxExcerpt = 400

	// requestTimeout bounds one HTTP call. The caller's context can end it
	// sooner. Logout uses its own short budget so a canceled create still
	// ends the session.
	requestTimeout = 30 * time.Second
	logoutTimeout  = 5 * time.Second

	userAgent = "agent-vm"
)

// labelPattern is one DNS label: lowercase letters, digits, and hyphens, not
// starting or ending with a hyphen, at most 63 characters.
var labelPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

// Password is a Pi-hole web password or application password. Formatting it
// yields a redacted marker, so a log, an error, or a %#v of a value that holds
// one cannot print the password.
type Password struct {
	raw string
}

func (p Password) String() string   { return "pihole-password" }
func (p Password) GoString() string { return "pihole-password" }

func (p Password) redact(text string) string {
	if p.raw == "" || text == "" {
		return text
	}
	return strings.ReplaceAll(text, p.raw, "pihole-password")
}

// ReadPassword reads a Pi-hole password from path. The file's contents are
// never included in an error; only the path is. The file must be a regular
// file, mode-restricted to the owner, and hold one line.
func ReadPassword(path string) (Password, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return Password{}, &config.ValidationError{
			Field: "pi-hole password file", Value: path, Err: err,
			Remedy: "Create a file containing the Pi-hole web password or an application password, mode 600, and pass it with --pihole-password-file.",
		}
	}
	if !info.Mode().IsRegular() {
		return Password{}, &config.ValidationError{
			Field: "pi-hole password file", Value: path,
			Err:    fmt.Errorf("must be a regular file"),
			Remedy: "Pass the password file itself, not a directory or a symbolic link.",
		}
	}
	if info.Mode().Perm()&0o077 != 0 {
		return Password{}, &config.ValidationError{
			Field: "pi-hole password file", Value: path,
			Err:    fmt.Errorf("is readable by group or others (mode %#o)", info.Mode().Perm()),
			Remedy: "Run chmod 600 on the file so only your user can read the password.",
		}
	}
	if info.Size() > maxPasswordLength {
		return Password{}, &config.ValidationError{
			Field: "pi-hole password file", Value: path,
			Err:    fmt.Errorf("must be at most %d bytes", maxPasswordLength),
			Remedy: "Put only the password in the file.",
		}
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return Password{}, &config.ValidationError{
			Field: "pi-hole password file", Value: path, Err: err,
			Remedy: "Create a file containing the Pi-hole web password or an application password, mode 600, and pass it with --pihole-password-file.",
		}
	}
	// A single trailing newline is how editors save a one-line file. Anything
	// else — a second line, a NUL, a bare carriage return — is not a password.
	if bytes.HasSuffix(contents, []byte("\n")) {
		contents = contents[:len(contents)-1]
	}
	if len(contents) == 0 || bytes.ContainsAny(contents, "\r\n\x00") {
		return Password{}, &config.ValidationError{
			Field: "pi-hole password file", Value: path,
			Err:    fmt.Errorf("must contain one password and nothing else"),
			Remedy: "Put the password on a single line. A trailing newline is fine.",
		}
	}
	return Password{raw: string(contents)}, nil
}

// Server is one Pi-hole v6 API. Password is sent only to POST /api/auth.
type Server struct {
	BaseURL  string
	Password Password
	// Insecure skips TLS verification. It is off unless the operator set it.
	// A LAN Pi-hole often presents a certificate nobody signed.
	Insecure bool
}

// SessionLeak is a DNS change Pi-hole accepted followed by a logout that
// failed. The record is in place. The session expires on its own, and the
// command must still record the name so a later destroy can remove it.
type SessionLeak struct {
	Name string
	Err  error
}

func (e *SessionLeak) Error() string {
	return fmt.Sprintf("Pi-hole accepted the change for %s, but logging out failed: %v\n  The session expires on its own, usually within a few minutes.", e.Name, e.Err)
}

func (e *SessionLeak) Unwrap() error { return e.Err }

// NormalizeAddress returns the canonical form of an IPv4 or IPv6 address.
// One CIDR suffix, as in the text virsh prints, is removed first.
func NormalizeAddress(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if i := strings.IndexByte(raw, '/'); i >= 0 {
		raw = raw[:i]
	}
	ip := net.ParseIP(raw)
	if ip == nil {
		return "", fmt.Errorf("invalid IP address %q", strings.TrimSpace(raw))
	}
	return ip.String(), nil
}

// RecordName is the DNS name create registers: the VM name, a dot, and the
// domain. An empty domain uses the default, agent, so the name is
// <vm>.agent. The result is checked again before it is placed in a URL.
func RecordName(vmName, domain string) (string, error) {
	if domain == "" {
		domain = config.DefaultPiHoleDomain
	}
	name := vmName + "." + domain
	if err := validateFQDN(name); err != nil {
		return "", &config.ValidationError{
			Field: "pi-hole domain", Value: domain, Err: err,
			Remedy: "Use a shorter domain. The full name must be a DNS name of at most 253 characters.",
		}
	}
	return name, nil
}

func validateFQDN(name string) error {
	if len(name) > 253 || strings.Contains(name, "..") {
		return fmt.Errorf("invalid DNS name %q", name)
	}
	labels := strings.Split(name, ".")
	if len(labels) < 2 {
		return fmt.Errorf("invalid DNS name %q", name)
	}
	for _, label := range labels {
		if !labelPattern.MatchString(label) {
			return fmt.Errorf("invalid DNS name %q", name)
		}
	}
	return nil
}

type hostLine struct {
	raw   string
	ip    string
	names []string
}

func parseHostLine(raw string) (hostLine, bool) {
	fields := strings.Fields(raw)
	if len(fields) < 2 {
		return hostLine{}, false
	}
	ip := net.ParseIP(fields[0])
	if ip == nil {
		return hostLine{}, false
	}
	return hostLine{raw: raw, ip: ip.String(), names: fields[1:]}, true
}

func hasName(names []string, name string) bool {
	for _, candidate := range names {
		if strings.EqualFold(candidate, name) {
			return true
		}
	}
	return false
}

func dropName(names []string, name string) []string {
	kept := make([]string, 0, len(names))
	for _, candidate := range names {
		if strings.EqualFold(candidate, name) {
			continue
		}
		kept = append(kept, candidate)
	}
	return kept
}

// Ensure makes name resolve to ip. A line that already says so is left alone.
// A line that points the name at another address loses the name, and any other
// names on that line are written back. The stored text is what Pi-hole matches
// on delete, so a line is removed exactly as GET returned it.
func (s Server) Ensure(ctx context.Context, name, ip string) error {
	if err := validateFQDN(name); err != nil {
		return err
	}
	canonical, err := NormalizeAddress(ip)
	if err != nil {
		return err
	}
	return s.withSession(ctx, name, func(ctx context.Context, client *http.Client, sid string) error {
		lines, err := s.getHosts(ctx, client, sid)
		if err != nil {
			return err
		}
		var deletes []string
		var replacements []string
		present := false
		for _, raw := range lines {
			line, ok := parseHostLine(raw)
			if !ok || !hasName(line.names, name) {
				continue
			}
			if line.ip == canonical {
				present = true
				continue
			}
			deletes = append(deletes, raw)
			if rest := dropName(line.names, name); len(rest) > 0 {
				replacements = append(replacements, line.ip+" "+strings.Join(rest, " "))
			}
		}
		if present && len(deletes) == 0 {
			return nil
		}
		for _, raw := range deletes {
			if err := s.deleteHost(ctx, client, sid, raw); err != nil {
				return err
			}
		}
		for _, line := range replacements {
			if err := s.putHost(ctx, client, sid, line); err != nil {
				return err
			}
		}
		if present {
			return nil
		}
		return s.putHost(ctx, client, sid, canonical+" "+name)
	})
}

// Remove drops name from every local DNS line. Other names on a shared line
// stay, at the address that line already had. A name Pi-hole does not have is
// success.
func (s Server) Remove(ctx context.Context, name string) error {
	if err := validateFQDN(name); err != nil {
		return err
	}
	return s.withSession(ctx, name, func(ctx context.Context, client *http.Client, sid string) error {
		lines, err := s.getHosts(ctx, client, sid)
		if err != nil {
			return err
		}
		for _, raw := range lines {
			line, ok := parseHostLine(raw)
			if !ok || !hasName(line.names, name) {
				continue
			}
			if err := s.deleteHost(ctx, client, sid, raw); err != nil {
				return err
			}
			if rest := dropName(line.names, name); len(rest) > 0 {
				if err := s.putHost(ctx, client, sid, line.ip+" "+strings.Join(rest, " ")); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

func (s Server) withSession(ctx context.Context, name string, fn func(context.Context, *http.Client, string) error) error {
	client := s.httpClient()
	sid, err := s.login(ctx, client)
	if err != nil {
		return err
	}
	opErr := fn(ctx, client, sid)
	logoutErr := s.logout(ctx, client, sid)
	if opErr != nil && logoutErr != nil {
		return fmt.Errorf("%w\n  logging out also failed: %v", opErr, logoutErr)
	}
	if opErr != nil {
		return opErr
	}
	if logoutErr != nil {
		return &SessionLeak{Name: name, Err: logoutErr}
	}
	return nil
}

func (s Server) login(ctx context.Context, client *http.Client) (string, error) {
	payload, err := json.Marshal(struct {
		Password string `json:"password"`
	}{Password: s.Password.raw})
	if err != nil {
		return "", fmt.Errorf("encoding the Pi-hole login: %w", err)
	}
	endpoint, err := s.apiURL("/api/auth")
	if err != nil {
		return "", err
	}
	status, body, err := s.do(ctx, client, http.MethodPost, endpoint, "", payload)
	if err != nil {
		return "", fmt.Errorf("logging in to Pi-hole at %s: %w", s.BaseURL, err)
	}
	if status < 200 || status >= 300 {
		return "", s.httpError("logging in to Pi-hole", status, body)
	}
	var parsed struct {
		Session struct {
			Valid bool   `json:"valid"`
			SID   string `json:"sid"`
		} `json:"session"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil || !parsed.Session.Valid || parsed.Session.SID == "" {
		return "", fmt.Errorf("logging in to Pi-hole at %s returned no session\n  Check the password file and the URL", s.BaseURL)
	}
	return parsed.Session.SID, nil
}

func (s Server) logout(ctx context.Context, client *http.Client, sid string) error {
	logoutCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), logoutTimeout)
	defer cancel()
	endpoint, err := s.apiURL("/api/auth")
	if err != nil {
		return err
	}
	status, body, err := s.do(logoutCtx, client, http.MethodDelete, endpoint, sid, nil)
	if err != nil {
		return fmt.Errorf("logging out of Pi-hole at %s: %w", s.BaseURL, err)
	}
	switch status {
	case http.StatusOK, http.StatusNoContent, http.StatusUnauthorized:
		return nil
	default:
		return s.httpError("logging out of Pi-hole", status, body)
	}
}

func (s Server) getHosts(ctx context.Context, client *http.Client, sid string) ([]string, error) {
	endpoint, err := s.apiURL("/api/config/dns/hosts")
	if err != nil {
		return nil, err
	}
	status, body, err := s.do(ctx, client, http.MethodGet, endpoint, sid, nil)
	if err != nil {
		return nil, fmt.Errorf("reading Pi-hole local DNS at %s: %w", s.BaseURL, err)
	}
	if status < 200 || status >= 300 {
		return nil, s.httpError("reading Pi-hole local DNS", status, body)
	}
	var parsed struct {
		Config struct {
			DNS struct {
				Hosts []string `json:"hosts"`
			} `json:"dns"`
		} `json:"config"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("reading Pi-hole local DNS at %s: the response was not the v6 config document", s.BaseURL)
	}
	return parsed.Config.DNS.Hosts, nil
}

func (s Server) putHost(ctx context.Context, client *http.Client, sid, item string) error {
	endpoint, err := s.hostItemURL(item)
	if err != nil {
		return err
	}
	status, body, err := s.do(ctx, client, http.MethodPut, endpoint, sid, nil)
	if err != nil {
		return fmt.Errorf("adding a Pi-hole DNS record at %s: %w", s.BaseURL, err)
	}
	if status == http.StatusBadRequest && strings.Contains(strings.ToLower(string(body)), "already") {
		return nil
	}
	if status < 200 || status >= 300 {
		return s.httpError("adding a Pi-hole DNS record", status, body)
	}
	return nil
}

func (s Server) deleteHost(ctx context.Context, client *http.Client, sid, item string) error {
	endpoint, err := s.hostItemURL(item)
	if err != nil {
		return err
	}
	status, body, err := s.do(ctx, client, http.MethodDelete, endpoint, sid, nil)
	if err != nil {
		return fmt.Errorf("removing a Pi-hole DNS record at %s: %w", s.BaseURL, err)
	}
	if status == http.StatusNotFound {
		return nil
	}
	if status < 200 || status >= 300 {
		return s.httpError("removing a Pi-hole DNS record", status, body)
	}
	return nil
}

func (s Server) do(ctx context.Context, client *http.Client, method, endpoint, sid string, payload []byte) (int, []byte, error) {
	var reader io.Reader
	if payload != nil {
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return 0, nil, s.Password.redactErr(err)
	}
	req.Header.Set("User-Agent", userAgent)
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if sid != "" {
		req.Header.Set("X-FTL-SID", sid)
		req.Header.Set("sid", sid)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, nil, s.Password.redactErr(err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody))
	if err != nil {
		return resp.StatusCode, nil, s.Password.redactErr(err)
	}
	if resp.StatusCode >= 300 && resp.StatusCode < 400 {
		return resp.StatusCode, body, nil
	}
	return resp.StatusCode, body, nil
}

func (p Password) redactErr(err error) error {
	if err == nil {
		return nil
	}
	redacted := p.redact(err.Error())
	if redacted == err.Error() {
		return err
	}
	return errors.New(redacted)
}

func (s Server) httpError(action string, status int, body []byte) error {
	excerpt := s.Password.redact(oneLine(body))
	msg := fmt.Sprintf("%s: Pi-hole at %s answered HTTP %d", action, s.BaseURL, status)
	if excerpt != "" {
		msg += ": " + excerpt
	}
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		msg += "\n  Pi-hole refused the password. An application password can change config only when webserver.api.app_sudo is true. Use the web password, or enable that setting."
	case status == http.StatusNotFound:
		msg += "\n  This client speaks Pi-hole v6 (PUT /api/config/dns/hosts). Pi-hole v5 is not supported."
	case status >= 300 && status < 400:
		msg += "\n  Configure the URL that answers the API directly. Redirects are not followed, so the password is not sent anywhere else."
	}
	return errors.New(msg)
}

func oneLine(body []byte) string {
	text := strings.Join(strings.Fields(string(body)), " ")
	if len(text) > maxExcerpt {
		return text[:maxExcerpt] + "..."
	}
	return text
}

func (s Server) apiURL(path string) (string, error) {
	base, err := url.Parse(s.BaseURL)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return "", fmt.Errorf("pi-hole url %q is not a base URL", s.BaseURL)
	}
	return base.ResolveReference(&url.URL{Path: path}).String(), nil
}

// hostItemURL is /api/config/dns/hosts/<url-escaped item>. The item is one
// hosts-file line, "IP name", and Pi-hole matches that text exactly. The
// space is %20. The path keeps slashes; the element is not encoded as
// dns%2Fhosts. url.JoinPath would encode the space and then encode it again,
// so the URL is built by hand.
func (s Server) hostItemURL(item string) (string, error) {
	base, err := url.Parse(strings.TrimRight(s.BaseURL, "/"))
	if err != nil || base.Scheme == "" || base.Host == "" {
		return "", fmt.Errorf("pi-hole url %q is not a base URL", s.BaseURL)
	}
	u := *base
	u.Path = "/api/config/dns/hosts/" + item
	u.RawPath = "/api/config/dns/hosts/" + url.PathEscape(item)
	u.RawQuery = ""
	u.Fragment = ""
	if u.EscapedPath() != u.RawPath {
		return "", fmt.Errorf("cannot encode the Pi-hole DNS record")
	}
	return u.String(), nil
}

func (s Server) httpClient() *http.Client {
	transport := cloneTransport()
	// The password is in the login body. A proxy configured on this machine
	// must not see it, and the operator named the Pi-hole URL directly.
	transport.Proxy = nil
	if s.Insecure {
		// Verification stays on unless the operator asked to skip it. A
		// self-signed certificate is common for a Pi-hole on a LAN, and that
		// is the only reason this exists.
		transport.TLSClientConfig = &tls.Config{
			InsecureSkipVerify: true,
			MinVersion:         tls.VersionTLS12,
		}
	}
	return &http.Client{
		Timeout:   requestTimeout,
		Transport: transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func cloneTransport() *http.Transport {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return &http.Transport{}
	}
	return base.Clone()
}
