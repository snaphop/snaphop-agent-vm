// Package tailscale is the guest-side join to a Tailscale network.
//
// The auth key is a credential. It is read from a file the operator names,
// handed to the guest on stdin, and then forgotten. It is never logged, never
// placed in an argument vector, and never written to a base image, a
// cloud-init seed, or vm.json (ADR-0014, SECURITY.md).
package tailscale

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/snaphop/snaphop-agent-vm/internal/config"
	"github.com/snaphop/snaphop-agent-vm/templates"
)

const (
	// ScriptPath is where the join script is copied on the guest. It matches
	// the other guest commands this tool installs under /usr/local/sbin.
	ScriptPath = "/usr/local/sbin/agent-vm-tailscale-join"

	// InstallTimeout bounds fetching and installing the package. The download
	// is the slow part; a guest that already has tailscale returns at once.
	InstallTimeout = 10 * time.Minute

	// UpTimeout bounds tailscale up. The script itself stops waiting after
	// 90s; this is the allowance around that, including the SSH session.
	UpTimeout = 3 * time.Minute
)

// hostnamePattern is a DNS label Tailscale will accept as a machine name:
// lowercase letters, digits, and hyphens, at most 63 characters.
var hostnamePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

// operatorPattern is the guest account allowed to operate tailscaled. It is
// the same shape as the login user cloud-init will create.
var operatorPattern = regexp.MustCompile(`^[a-z_][a-z0-9_-]{0,31}$`)

// tagPattern is one Tailscale ACL tag. Tags are passed through to
// --advertise-tags and must not be able to change the command around them.
var tagPattern = regexp.MustCompile(`^tag:[A-Za-z][A-Za-z0-9-]*$`)

// authKeyPattern is the shape of a single auth key, Tailscale's or another
// coordination server's, with no whitespace and no shell metacharacters.
// The key is still never placed in a shell command; the pattern is what
// keeps a PEM, a script, or an empty file from being sent to the guest.
// The upper bound is a length check: Go's regexp rejects a repeat that large.
var authKeyPattern = regexp.MustCompile(`^[A-Za-z0-9_./:+=-]{16,}$`)

// maxAuthKeyLength is how long an auth key file may be. A longer file is not
// a key, and it is not sent to the guest.
const maxAuthKeyLength = 4096

// AuthKey is a Tailscale auth key. Formatting it yields a redacted marker, so
// a log, an error, or a %#v of a value that holds one cannot print the key.
type AuthKey struct {
	raw string
}

func (k AuthKey) String() string   { return "tailscale-auth-key" }
func (k AuthKey) GoString() string { return "tailscale-auth-key" }

// Reader is the key as the guest's `up` command must receive it: on stdin,
// with no trailing commentary.
func (k AuthKey) Reader() io.Reader {
	return bytes.NewReader([]byte(k.raw))
}

// ReadAuthKey reads an auth key from path. The file's contents are never
// included in an error; only the path is.
func ReadAuthKey(path string) (AuthKey, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return AuthKey{}, &config.ValidationError{
			Field: "tailscale auth key file", Value: path, Err: err,
			Remedy: "Create an auth key in the tailnet admin console and pass its file with --tailscale-auth-key-file.",
		}
	}
	key := strings.TrimSpace(string(contents))
	if len(key) > maxAuthKeyLength || !authKeyPattern.MatchString(key) {
		return AuthKey{}, &config.ValidationError{
			Field: "tailscale auth key file", Value: path,
			Err:    fmt.Errorf("the file must contain one auth key and nothing else"),
			Remedy: "Put a single auth key in the file, with no quotes. A one-time ephemeral key is the right kind for a VM that will be destroyed.",
		}
	}
	return AuthKey{raw: key}, nil
}

// Options is the non-secret half of a join. A plan, a log, and vm.json can
// describe the join from this alone.
type Options struct {
	Hostname      string
	Operator      string
	LoginServer   string
	AdvertiseTags []string
	Ephemeral     bool
	SSH           bool
}

// Prepare checks a join and returns the options that will be sent to the
// guest and recorded. Tags are de-duplicated, order preserved.
func Prepare(opts Options) (Options, error) {
	opts.Hostname = strings.TrimSpace(opts.Hostname)
	opts.Operator = strings.TrimSpace(opts.Operator)
	opts.LoginServer = strings.TrimSpace(opts.LoginServer)

	if !hostnamePattern.MatchString(opts.Hostname) {
		return Options{}, &config.ValidationError{
			Field: "tailscale hostname", Value: opts.Hostname,
			Err:    fmt.Errorf("must be a lowercase name of at most 63 characters, using letters, digits, and hyphens"),
			Remedy: "Omit --tailscale-hostname to use the VM name, or pass a name such as agent-01.",
		}
	}
	if !operatorPattern.MatchString(opts.Operator) {
		return Options{}, &config.ValidationError{
			Field: "guest user", Value: opts.Operator,
			Err:    fmt.Errorf("cannot operate tailscaled"),
			Remedy: "Use a lowercase Linux account name such as `agent`.",
		}
	}
	if err := validateLoginServer(opts.LoginServer); err != nil {
		return Options{}, &config.ValidationError{
			Field: "tailscale login server", Value: opts.LoginServer,
			Err:    err,
			Remedy: "Pass an https URL with a host and no user or password, such as https://headscale.example.com. Omit the flag to use Tailscale's coordination server.",
		}
	}

	seen := map[string]bool{}
	tags := make([]string, 0, len(opts.AdvertiseTags))
	for _, tag := range opts.AdvertiseTags {
		tag = strings.TrimSpace(tag)
		if !tagPattern.MatchString(tag) {
			return Options{}, &config.ValidationError{
				Field: "tailscale tag", Value: tag,
				Err:    fmt.Errorf("must look like tag:ci"),
				Remedy: "Repeat --tailscale-advertise-tag once per tag. Tag names start with tag:.",
			}
		}
		if seen[tag] {
			continue
		}
		seen[tag] = true
		tags = append(tags, tag)
	}
	if len(tags) == 0 {
		opts.AdvertiseTags = nil
	} else {
		opts.AdvertiseTags = tags
	}
	return opts, nil
}

func validateLoginServer(raw string) error {
	if raw == "" {
		return nil
	}
	// The URL is placed in the guest command and in vm.json, so it must not
	// carry a credential and must not need shell quoting.
	if strings.ContainsAny(raw, " \t\r\n\"'\\$`;&|<>(){}") {
		return fmt.Errorf("must be an https URL with a host and no credentials")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return fmt.Errorf("must be an https URL with a host and no credentials")
	}
	return nil
}

// Script is the guest program create copies over SSH.
func Script() ([]byte, error) {
	script, err := templates.FS.ReadFile("distro/tailscale-join.sh")
	if err != nil {
		return nil, fmt.Errorf("reading the embedded Tailscale join script: %w", err)
	}
	return script, nil
}

// CopyCommand writes the join script onto the guest. The script bytes are
// the command's stdin, not an argument.
func CopyCommand() []string {
	return []string{"sudo", "-n", "tee", ScriptPath}
}

// ChmodCommand makes the copied script executable.
func ChmodCommand() []string {
	return []string{"sudo", "-n", "chmod", "755", ScriptPath}
}

// InstallCommand installs Tailscale when the guest does not have it. It must
// be run before the auth key is sent.
func InstallCommand() []string {
	return []string{"sudo", "-n", ScriptPath, "install"}
}

// UpCommand joins the tailnet. The auth key is the command's stdin, not an
// argument: argv is logged, and a guest process list would show it too.
func UpCommand(opts Options) []string {
	args := []string{
		"sudo", "-n", ScriptPath, "up",
		"--hostname=" + opts.Hostname,
		"--operator=" + opts.Operator,
	}
	if opts.Ephemeral {
		args = append(args, "--ephemeral")
	}
	if opts.SSH {
		args = append(args, "--ssh")
	}
	if opts.LoginServer != "" {
		args = append(args, "--login-server="+opts.LoginServer)
	}
	if len(opts.AdvertiseTags) > 0 {
		args = append(args, "--advertise-tags="+strings.Join(opts.AdvertiseTags, ","))
	}
	return args
}

// IPv4FromOutput returns the last IPv4 address in the join script's stdout.
// Anything else the guest printed is ignored. The address is display only;
// it is not a destination this tool will connect to.
func IPv4FromOutput(out []byte) string {
	found := ""
	for _, line := range bytes.Split(out, []byte("\n")) {
		text := strings.TrimSpace(string(line))
		ip := net.ParseIP(text)
		if ip == nil || ip.To4() == nil {
			continue
		}
		found = ip.To4().String()
	}
	return found
}
