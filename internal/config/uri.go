package config

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

// A libvirt URI says which hypervisor to drive and how to reach it. When it
// names another machine, it also decides where every host tool runs: agent-vm
// builds base images, creates overlays, and defines domains on the machine that
// owns the hypervisor, not on the one the operator typed the command on
// (ADR-0010). Parsing the URI is therefore not a formatting detail — it is what
// selects the execution model for the whole run.

// SSHTransport is the only remote transport agent-vm drives. libvirt itself
// speaks several more; this one is singled out because agent-vm needs a shell
// on the hypervisor host, not just a libvirt connection.
const SSHTransport = "ssh"

// Connection is a resolved libvirt URI: which hypervisor to talk to, and — when
// it is another machine — how to reach a shell on it.
type Connection struct {
	// URI is the connection string as given, passed to virsh and virt-install
	// unchanged.
	URI string
	// Remote reports whether the hypervisor is another machine. When it is
	// false every host tool runs here, which is the default and the case every
	// existing deployment is in.
	Remote bool
	// SSHDestination is the [user@]host that ssh connects to. Empty for a local
	// connection.
	SSHDestination string
	// SSHPort is the port from the URI, or 0 for ssh's default. It is separate
	// from SSHDestination because ssh takes it as -p rather than as part of the
	// destination.
	SSHPort int
	// IdentityFile is the private key named by the URI's keyfile parameter, so
	// that agent-vm's own ssh invocations authenticate the same way libvirt's
	// do. The file is named to ssh and never read here.
	IdentityFile string
	// driver and path are the URI's two local halves — "qemu" and "/system" —
	// kept so that HypervisorURI can spell the same connection as it reads on
	// the hypervisor itself.
	driver string
	path   string
	// NoVerify mirrors the URI's no_verify parameter: the operator has already
	// told libvirt not to check the host's key, and an agent-vm ssh that
	// insisted on checking it would fail where libvirt succeeds.
	NoVerify bool
}

// ParseConnection reads a libvirt URI. It rejects the transports agent-vm
// cannot drive rather than accepting them and failing later, halfway through a
// create, with an error from a tool that ran on the wrong machine.
func ParseConnection(uri string) (*Connection, error) {
	if uri == "" {
		return nil, &ValidationError{Field: "libvirt_uri", Value: "", Err: fmt.Errorf("must not be empty")}
	}

	parsed, err := url.Parse(uri)
	if err != nil {
		return nil, &ValidationError{Field: "libvirt_uri", Value: uri, Err: err}
	}

	// libvirt spells the transport as a "+suffix" on the scheme:
	// qemu+ssh://host/system. No suffix means a local connection.
	driver, transport, hasTransport := strings.Cut(parsed.Scheme, "+")
	if driver == "" {
		return nil, &ValidationError{
			Field: "libvirt_uri", Value: uri,
			Err:    fmt.Errorf("no hypervisor driver in the URI"),
			Remedy: "A libvirt URI looks like qemu:///system or qemu+ssh://user@host/system.",
		}
	}

	conn := &Connection{URI: uri, driver: driver, path: parsed.Path}
	if !hasTransport && parsed.Host == "" {
		// qemu:///system, qemu:///session: this machine.
		return conn, nil
	}
	if !hasTransport {
		// qemu://host/system is libvirt's TLS transport spelled without a
		// suffix. It reaches libvirt but gives no shell on the hypervisor.
		return nil, errUnsupportedTransport(uri, "tls")
	}
	if transport != SSHTransport {
		return nil, errUnsupportedTransport(uri, transport)
	}
	if parsed.Host == "" {
		return nil, &ValidationError{
			Field: "libvirt_uri", Value: uri,
			Err:    fmt.Errorf("the ssh transport needs a host to connect to"),
			Remedy: "Name the hypervisor: qemu+ssh://user@host/system.",
		}
	}

	conn.Remote = true
	conn.SSHDestination = parsed.Hostname()
	if user := parsed.User.Username(); user != "" {
		conn.SSHDestination = user + "@" + conn.SSHDestination
	}
	// A password in a libvirt URI is not something agent-vm can hand to ssh,
	// and quietly ignoring it would leave the operator wondering why they are
	// prompted. Key-based authentication is the supported path (SECURITY.md).
	if _, set := parsed.User.Password(); set {
		return nil, &ValidationError{
			Field: "libvirt_uri", Value: uri,
			Err:    fmt.Errorf("a password in the URI is not supported"),
			Remedy: "Use key-based SSH authentication, and name the key with ?keyfile=<path> if it is not one ssh offers by default.",
		}
	}
	if port := parsed.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return nil, &ValidationError{Field: "libvirt_uri", Value: uri, Err: fmt.Errorf("%q is not a valid port", port)}
		}
		conn.SSHPort = n
	}

	query := parsed.Query()
	conn.IdentityFile = query.Get("keyfile")
	conn.NoVerify = query.Get("no_verify") == "1"
	return conn, nil
}

func errUnsupportedTransport(uri, transport string) error {
	return &ValidationError{
		Field: "libvirt_uri", Value: uri,
		Err: fmt.Errorf("the %s transport is not supported", transport),
		Remedy: "agent-vm needs a shell on the hypervisor to build images and create disks there, " +
			"so a remote connection must use ssh: qemu+ssh://user@host/system.",
	}
}

// HypervisorURI is the connection as it reads on the machine the hypervisor
// runs on: the same driver and the same path, with the transport dropped.
//
// It is what virsh and virt-install are given, because they run on the
// hypervisor rather than here (ADR-0010). Handing them the remote URI instead
// would have them connect back over ssh to the machine they are already on,
// and would still leave every path argument pointing at the wrong filesystem.
// For a local connection this is the URI unchanged.
func (c *Connection) HypervisorURI() string {
	if !c.Remote {
		return c.URI
	}
	path := c.path
	if path == "" {
		path = "/"
	}
	return c.driver + "://" + path
}

// Connection resolves this configuration's libvirt URI. It is parsed on every
// call rather than cached: the result is small, and a Config that carried a
// parsed copy could drift from the URI beside it.
func (c *Config) Connection() (*Connection, error) { return ParseConnection(c.LibvirtURI) }

// RemoteHypervisor reports whether host tools run on another machine. Callers
// that only need the yes-or-no answer use it so that a malformed URI, which
// validation has already rejected, does not need handling twice.
func (c *Config) RemoteHypervisor() bool {
	conn, err := c.Connection()
	return err == nil && conn.Remote
}

// SessionMode reports whether the connection runs QEMU as the connecting user
// rather than as libvirt's own account. It is a property of the URI's path
// rather than of the whole string, so it holds for a remote session connection
// as well as for the local qemu:///session.
func (c *Config) SessionMode() bool {
	parsed, err := url.Parse(c.LibvirtURI)
	if err != nil {
		return false
	}
	return strings.TrimSuffix(parsed.Path, "/") == "/session"
}
