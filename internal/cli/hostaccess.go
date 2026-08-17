package cli

import (
	"encoding/binary"
	"fmt"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// Under a privileged libvirt the QEMU process does not run as the invoking
// user, so it must be able to traverse every directory between / and a VM's
// disk. The default state directory lives under ~/.local/share, and a home
// directory is commonly 0700 or 0710 — which makes this the most likely reason
// a create fails late, with `Cannot access storage file ... Permission denied`
// from virt-install after the overlay and the domain already exist. doctor
// checks it up front so the failure is reported before anything is built.

// qemuConfPath is libvirt's QEMU driver configuration, read to learn which user
// the hypervisor runs as. It is only read, never written.
const qemuConfPath = "/etc/libvirt/qemu.conf"

// qemuUserCandidates are the account names distributions use for the QEMU
// process when qemu.conf does not set one explicitly: Debian and Ubuntu use
// libvirt-qemu, Fedora and RHEL use qemu, and some hosts fall back to nobody.
// They are probed in order and the first that exists wins.
var qemuUserCandidates = []string{"libvirt-qemu", "qemu", "nobody"}

// hypervisorIdentity is the user a privileged libvirt runs QEMU as, expanded to
// the group set that a permission decision needs.
type hypervisorIdentity struct {
	Name string
	UID  uint32
	GIDs map[uint32]bool
}

// resolveHypervisorIdentity reports the account QEMU will run as. It returns a
// nil identity with a nil error when the host does not let us determine one; a
// guess would produce a confident and wrong permission verdict, so the caller
// skips the check instead.
func resolveHypervisorIdentity(confPath string) (*hypervisorIdentity, error) {
	name := qemuUserFromConf(confPath)
	if name != "" {
		return lookupIdentity(name)
	}
	for _, candidate := range qemuUserCandidates {
		id, err := lookupIdentity(candidate)
		if err != nil {
			continue
		}
		return id, nil
	}
	return nil, nil
}

func lookupIdentity(name string) (*hypervisorIdentity, error) {
	u, err := user.Lookup(name)
	if err != nil {
		// qemu.conf accepts a numeric id as well as a name.
		if _, convErr := strconv.ParseUint(name, 10, 32); convErr == nil {
			u, err = user.LookupId(name)
		}
		if err != nil {
			return nil, fmt.Errorf("looking up the hypervisor user %q: %w", name, err)
		}
	}

	uid, err := strconv.ParseUint(u.Uid, 10, 32)
	if err != nil {
		return nil, fmt.Errorf("user %q has an unparseable uid %q: %w", name, u.Uid, err)
	}

	gids := map[uint32]bool{}
	groupIDs, err := u.GroupIds()
	if err != nil {
		// Supplementary groups are unavailable on some hosts; the primary group
		// is still enough to answer most permission questions.
		groupIDs = []string{u.Gid}
	}
	for _, raw := range groupIDs {
		if gid, err := strconv.ParseUint(raw, 10, 32); err == nil {
			gids[uint32(gid)] = true
		}
	}

	return &hypervisorIdentity{Name: u.Username, UID: uint32(uid), GIDs: gids}, nil
}

// hypervisorIdentity resolves the account QEMU runs as, through the test seam
// when one is set.
func (a *App) hypervisorIdentity() (*hypervisorIdentity, error) {
	if a.HypervisorIdentity != nil {
		return a.HypervisorIdentity()
	}
	return resolveHypervisorIdentity(qemuConfPath)
}

// qemuUserFromConf extracts an explicit `user = "name"` setting from qemu.conf.
// It returns "" when the file is absent or the setting is commented out, which
// means the distribution default applies.
func qemuUserFromConf(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(key) != "user" {
			continue
		}
		return strings.Trim(strings.TrimSpace(value), `"'`)
	}
	return ""
}

// firstUntraversable returns the shallowest ancestor of dir that the identity
// cannot search, or "" when the whole path is traversable. Directories that do
// not exist yet are ignored: create makes them, inheriting a parent we have
// already checked.
func firstUntraversable(dir string, id *hypervisorIdentity) (string, error) {
	absolute, err := filepath.Abs(dir)
	if err != nil {
		return "", fmt.Errorf("resolving %s: %w", dir, err)
	}
	// Symlinks are resolved because the hypervisor traverses the real path, not
	// the one we were given. Only the existing prefix can be resolved.
	if resolved, err := filepath.EvalSymlinks(absolute); err == nil {
		absolute = resolved
	}

	for _, ancestor := range ancestors(absolute) {
		info, err := os.Stat(ancestor)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("inspecting %s: %w", ancestor, err)
		}
		searchable, err := canSearch(ancestor, info, id)
		if err != nil {
			return "", err
		}
		if !searchable {
			return ancestor, nil
		}
	}
	return "", nil
}

// ancestors lists every directory from the root down to path, inclusive.
func ancestors(path string) []string {
	parts := strings.Split(strings.TrimPrefix(path, string(filepath.Separator)), string(filepath.Separator))
	list := []string{string(filepath.Separator)}
	current := ""
	for _, part := range parts {
		if part == "" {
			continue
		}
		current += string(filepath.Separator) + part
		list = append(list, current)
	}
	return list
}

// canSearch answers whether the identity holds execute permission on a
// directory, honouring a POSIX ACL when one is present. Permission bits alone
// would report a false failure on a host that granted access with setfacl,
// which is the remedy this very check recommends.
func canSearch(path string, info os.FileInfo, id *hypervisorIdentity) (bool, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return false, fmt.Errorf("cannot read ownership of %s", path)
	}

	if entries, err := readACL(path); err != nil {
		return false, err
	} else if len(entries) > 0 {
		return aclGrantsSearch(entries, stat, id), nil
	}

	mode := info.Mode().Perm()
	switch {
	case stat.Uid == id.UID:
		return mode&0o100 != 0, nil
	case id.GIDs[stat.Gid]:
		return mode&0o010 != 0, nil
	default:
		return mode&0o001 != 0, nil
	}
}

// POSIX ACL entry tags and permission bits, as encoded in the
// system.posix_acl_access extended attribute (uapi/linux/posix_acl_xattr.h).
const (
	aclVersion    = 2
	aclEntrySize  = 8
	aclHeaderSize = 4

	tagUserObj  = 0x01
	tagUser     = 0x02
	tagGroupObj = 0x04
	tagGroup    = 0x08
	tagMask     = 0x10
	tagOther    = 0x20

	permExecute = 0x01
)

type aclEntry struct {
	Tag  uint16
	Perm uint16
	ID   uint32
}

// readACL returns the access ACL of a path, or nil when it has none. A missing
// attribute is the common case and is not an error.
func readACL(path string) ([]aclEntry, error) {
	const attr = "system.posix_acl_access"

	size, err := syscall.Getxattr(path, attr, nil)
	if err != nil || size <= 0 {
		return nil, nil
	}
	buf := make([]byte, size)
	size, err = syscall.Getxattr(path, attr, buf)
	if err != nil || size <= 0 {
		return nil, nil
	}
	return parseACL(buf[:size])
}

// parseACL decodes the binary access-ACL representation. An attribute we cannot
// decode is reported rather than ignored: silently treating it as absent would
// fall back to permission bits and report a failure the host does not have.
func parseACL(data []byte) ([]aclEntry, error) {
	if len(data) < aclHeaderSize {
		return nil, fmt.Errorf("posix acl is %d bytes, too short to hold a header", len(data))
	}
	if version := binary.LittleEndian.Uint32(data[:aclHeaderSize]); version != aclVersion {
		return nil, fmt.Errorf("posix acl version %d is not the supported version %d", version, aclVersion)
	}

	body := data[aclHeaderSize:]
	if len(body)%aclEntrySize != 0 {
		return nil, fmt.Errorf("posix acl body is %d bytes, not a multiple of the %d-byte entry", len(body), aclEntrySize)
	}

	entries := make([]aclEntry, 0, len(body)/aclEntrySize)
	for offset := 0; offset < len(body); offset += aclEntrySize {
		entry := body[offset : offset+aclEntrySize]
		entries = append(entries, aclEntry{
			Tag:  binary.LittleEndian.Uint16(entry[0:2]),
			Perm: binary.LittleEndian.Uint16(entry[2:4]),
			ID:   binary.LittleEndian.Uint32(entry[4:8]),
		})
	}
	return entries, nil
}

// aclGrantsSearch applies the POSIX.1e access check for the execute bit: the
// owner entry wins outright, then a matching named-user entry, then the union
// of the owning and named groups, and finally the other entry. Every entry
// except the owner's and other's is capped by the mask.
func aclGrantsSearch(entries []aclEntry, stat *syscall.Stat_t, id *hypervisorIdentity) bool {
	var (
		mask       uint16 = permExecute // absent mask restricts nothing
		haveMask   bool
		other      uint16
		userObj    uint16
		namedUser  uint16
		haveNamed  bool
		groupPerms uint16
		haveGroup  bool
	)

	for _, entry := range entries {
		switch entry.Tag {
		case tagMask:
			mask, haveMask = entry.Perm, true
		case tagOther:
			other = entry.Perm
		case tagUserObj:
			userObj = entry.Perm
		case tagUser:
			if entry.ID == id.UID {
				namedUser, haveNamed = entry.Perm, true
			}
		case tagGroupObj:
			if id.GIDs[stat.Gid] {
				groupPerms |= entry.Perm
				haveGroup = true
			}
		case tagGroup:
			if id.GIDs[entry.ID] {
				groupPerms |= entry.Perm
				haveGroup = true
			}
		}
	}
	if !haveMask {
		mask = ^uint16(0)
	}

	switch {
	case stat.Uid == id.UID:
		return userObj&permExecute != 0
	case haveNamed:
		return namedUser&mask&permExecute != 0
	case haveGroup:
		return groupPerms&mask&permExecute != 0
	default:
		return other&permExecute != 0
	}
}
