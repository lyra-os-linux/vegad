package parental

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

const installerPermitLimit = 1024
const installerPermitAvailable = "available"
const installerPermitConsumed = "consumed"
const InstalledESPEnrollmentPermitDirectory = "/boot/efi/EFI/lyra/parental-enrollment"
const InstalledParentalAuthorityDirectory = "/var/lib/lyra/parental-authority"
const InstalledParentalSnapshotScope = "/var/lib/lyra/parental"
const InstalledParentalCatalogDirectory = "/var/lib/lyra/parental/catalog"
const InstalledParentalInventoryDirectory = "/var/lib/lyra/parental/inventory"
const InstalledParentalLedgerDirectory = "/var/lib/lyra/parental/ledger"
const InstalledParentalSnapperConfig = "/etc/snapper/configs/lyra-parental"

type installerPermit struct {
	installation string
	operation    string
	state        string
	digest       string
}

// ESPInstallerPermission binds a one-use installer record on the separately
// mounted EFI System Partition to the Btrfs authority and catalog on the root
// filesystem. Its callbacks implement the contracts for
// NewInstallerReferenceProvisioner; they authenticate local root-managed state,
// not a cryptographic issuer. Ancestors of all configured paths are trusted.
type ESPInstallerPermission struct {
	directory, authority, catalog, inventory, ledger string
	owner                                            uint32
}

func NewESPInstallerPermission() *ESPInstallerPermission {
	return &ESPInstallerPermission{
		directory: InstalledESPEnrollmentPermitDirectory,
		authority: InstalledParentalAuthorityDirectory,
		catalog:   InstalledParentalCatalogDirectory,
		inventory: InstalledParentalInventoryDirectory,
		ledger:    InstalledParentalLedgerDirectory,
		owner:     0,
	}
}

func installedParentalDomain() *BtrfsSnapshotDomain {
	return NewBtrfsSnapshotDomain(InstalledParentalSnapshotScope)
}

func installedParentalSnapperDomain() *SnapperSnapshotDomain {
	return NewSnapperSnapshotDomain(InstalledParentalSnapperConfig, InstalledParentalSnapshotScope)
}

// installedParentalLayoutReady remains false until the installer creates and
// qualifies InstalledParentalSnapshotScope as an independent Btrfs subvolume,
// with catalog/inventory/ledger inside that Snapper scope and the authority in
// a sibling subvolume, then qualifies reboot and rollback behavior. Do not turn
// this on merely because the paths happen to exist.
func installedParentalLayoutReady() bool { return false }

func runningInstallation() (string, error) {
	data, e := os.ReadFile("/etc/machine-id")
	if e != nil {
		return "", Denied
	}
	installation := strings.TrimSpace(string(data))
	if !token.MatchString(installation) {
		return "", Denied
	}
	return installation, nil
}

// NewESPBackedReferenceProvisioner composes the installer permission reader
// and durable consumer with the first-reference writer. Issuer, topology and
// maintenance authorization remain trusted service configuration/callbacks.
func NewESPBackedReferenceProvisioner(issuer string) (*ReferenceProvisioner, *ESPInstallerPermission) {
	authority := InstalledParentalAuthorityDirectory
	catalog := InstalledParentalCatalogDirectory
	qualifier := installedParentalSnapperDomain().Qualify
	permission := NewESPInstallerPermission()
	if !installedParentalLayoutReady() {
		return nil, permission
	}
	provisioner := NewInstallerReferenceProvisioner(authority, catalog, issuer, qualifier, permission.CheckFresh, permission.Consume)
	return provisioner, permission
}

// AvailableOperation returns the one-use token for the running installation.
// The caller binds this token to the trusted initial reference it obtains from
// the configured issuer before invoking Initialize.
func (s *ESPInstallerPermission) AvailableOperation(installation string) (string, error) {
	active, operation, consumed, e := s.CurrentEnrollment()
	if e != nil {
		return "", e
	}
	if active != installation || consumed || !token.MatchString(installation) {
		return "", Denied
	}
	return operation, nil
}

// CurrentEnrollment returns the operation token and whether its ESP permit
// has already been consumed. A consumed permit is usable only to resume the
// exact digest-bound transaction through ReferenceProvisioner.ResumeInitial.
func (s *ESPInstallerPermission) CurrentEnrollment() (string, string, bool, error) {
	installation, e := runningInstallation()
	if e != nil {
		return "", "", false, e
	}
	dir, _, e := s.openDirectory()
	if e != nil {
		return "", "", false, e
	}
	defer unix.Close(dir)
	data, _, e := readPermitAt(dir, installation+".json", s.owner)
	if e != nil {
		return "", "", false, e
	}
	operation, consumed, e := enrollmentOperationFromPermit(data, installation)
	if e != nil {
		return "", "", false, e
	}
	return installation, operation, consumed, nil
}

func enrollmentOperationFromPermit(data []byte, installation string) (string, bool, error) {
	if !token.MatchString(installation) {
		return "", false, Denied
	}
	p, e := decodeInstallerPermit(data)
	if e != nil {
		return "", false, e
	}
	if p.installation != installation || !token.MatchString(p.operation) {
		return "", false, Denied
	}
	return p.operation, p.state == installerPermitConsumed, nil
}

func permitBytes(p installerPermit) []byte {
	if p.state == installerPermitAvailable {
		data, _ := json.Marshal(map[string]any{"schema": 1, "installation": p.installation, "operation": p.operation, "state": p.state})
		return data
	}
	data, _ := json.Marshal(map[string]any{"schema": 1, "installation": p.installation, "operation": p.operation, "state": p.state, "reference_digest": p.digest})
	return data
}

func decodeInstallerPermit(data []byte) (installerPermit, error) {
	if len(data) == 0 || len(data) > installerPermitLimit || !utf8.Valid(data) {
		return installerPermit{}, InvalidReference
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	raw, e := readJSON(d, 0)
	if e != nil {
		return installerPermit{}, InvalidReference
	}
	if _, e = d.Token(); e != io.EOF {
		return installerPermit{}, InvalidReference
	}
	row, ok := object(raw, "schema", "installation", "operation", "state")
	if !ok {
		row, ok = object(raw, "schema", "installation", "operation", "state", "reference_digest")
	}
	if !ok {
		return installerPermit{}, InvalidReference
	}
	if schema, valid := positive(row["schema"], 1); !valid || schema != 1 {
		return installerPermit{}, InvalidReference
	}
	p := installerPermit{}
	if p.installation, ok = matching(row["installation"], token); !ok {
		return installerPermit{}, InvalidReference
	}
	if p.operation, ok = matching(row["operation"], token); !ok {
		return installerPermit{}, InvalidReference
	}
	if p.state, ok = oneOf(row["state"], installerPermitAvailable, installerPermitConsumed); !ok {
		return installerPermit{}, InvalidReference
	}
	if p.state == installerPermitAvailable {
		if _, hasDigest := row["reference_digest"]; hasDigest {
			return installerPermit{}, InvalidReference
		}
	} else if p.digest, ok = matching(row["reference_digest"], hash); !ok {
		return installerPermit{}, InvalidReference
	}
	return p, nil
}

func (s *ESPInstallerPermission) openDirectory() (int, unix.Stat_t, error) {
	if s == nil || !canonicalDomainPath(s.directory) || !canonicalDomainPath(s.authority) || !canonicalDomainPath(s.catalog) {
		return -1, unix.Stat_t{}, RecoveryRequired
	}
	fd, e := unix.Open(s.directory, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if e != nil {
		return -1, unix.Stat_t{}, RecoveryRequired
	}
	var esp, authority, catalog unix.Stat_t
	var espFS, authorityFS, catalogFS unix.Statfs_t
	if unix.Fstat(fd, &esp) != nil || unix.Fstatfs(fd, &espFS) != nil ||
		unix.Stat(s.authority, &authority) != nil || unix.Statfs(s.authority, &authorityFS) != nil ||
		unix.Stat(s.catalog, &catalog) != nil || unix.Statfs(s.catalog, &catalogFS) != nil {
		unix.Close(fd)
		return -1, unix.Stat_t{}, RecoveryRequired
	}
	if esp.Uid != s.owner || esp.Mode&07777 != 0700 || espFS.Type != unix.MSDOS_SUPER_MAGIC ||
		authorityFS.Type != unix.BTRFS_SUPER_MAGIC || catalogFS.Type != unix.BTRFS_SUPER_MAGIC ||
		authority.Dev != catalog.Dev || esp.Dev == authority.Dev || esp.Dev == catalog.Dev {
		unix.Close(fd)
		return -1, unix.Stat_t{}, RecoveryRequired
	}
	return fd, esp, nil
}

func readPermitAt(dir int, name string, owner uint32) ([]byte, unix.Stat_t, error) {
	fd, e := unix.Openat(dir, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if errors.Is(e, unix.ENOENT) {
		return nil, unix.Stat_t{}, RecoveryRequired
	}
	if e != nil {
		return nil, unix.Stat_t{}, UnsafeState
	}
	defer unix.Close(fd)
	var before unix.Stat_t
	if unix.Fstat(fd, &before) != nil || !safeFile(&before, owner) || before.Size <= 0 || before.Size > installerPermitLimit {
		return nil, unix.Stat_t{}, UnsafeState
	}
	data := make([]byte, int(before.Size))
	for offset := 0; offset < len(data); {
		n, err := unix.Read(fd, data[offset:])
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil || n <= 0 {
			return nil, unix.Stat_t{}, UnsafeState
		}
		offset += n
	}
	var after unix.Stat_t
	if unix.Fstat(fd, &after) != nil || !safeFile(&after, owner) || before.Size != after.Size || before.Mtim != after.Mtim || before.Ctim != after.Ctim || !sameEntry(dir, name, &before) {
		return nil, unix.Stat_t{}, UnsafeState
	}
	return data, before, nil
}

func (s *ESPInstallerPermission) check(installation, operation, digest string) error {
	if !token.MatchString(installation) || !token.MatchString(operation) || !hash.MatchString(digest) {
		return Denied
	}
	active, e := runningInstallation()
	if e != nil || active != installation {
		return Denied
	}
	dir, _, e := s.openDirectory()
	if e != nil {
		return e
	}
	defer unix.Close(dir)
	data, _, e := readPermitAt(dir, installation+".json", s.owner)
	if e != nil {
		return e
	}
	p, e := decodeInstallerPermit(data)
	if e != nil {
		return e
	}
	if p.installation != installation || p.operation != operation {
		return Denied
	}
	if p.state == installerPermitConsumed && p.digest == digest {
		return InstallerPermissionConsumed
	}
	if p.state != installerPermitAvailable {
		return Denied
	}
	return nil
}

// CheckFresh is suitable as proveFresh for NewInstallerReferenceProvisioner.
func (s *ESPInstallerPermission) CheckFresh(installation, operation, digest string) error {
	return s.check(installation, operation, digest)
}

// Consume is suitable as consume for NewInstallerReferenceProvisioner. The
// committed state and complete reference digest live on the ESP, outside both
// Btrfs snapshot domains. A rename or fsync uncertainty leaves the proof
// consumed or ambiguous; it never recreates an available permission.
func (s *ESPInstallerPermission) Consume(installation, operation, digest string) error {
	if !token.MatchString(installation) || !token.MatchString(operation) || !hash.MatchString(digest) {
		return Denied
	}
	active, e := runningInstallation()
	if e != nil || active != installation {
		return Denied
	}
	dir, directoryStat, e := s.openDirectory()
	if e != nil {
		return e
	}
	defer unix.Close(dir)
	name := installation + ".json"
	data, original, e := readPermitAt(dir, name, s.owner)
	if e != nil {
		return e
	}
	p, e := decodeInstallerPermit(data)
	if e != nil {
		return e
	}
	if p.installation != installation || p.operation != operation || p.state != installerPermitAvailable {
		return Denied
	}
	replacement := permitBytes(installerPermit{installation: installation, operation: operation, state: installerPermitConsumed, digest: digest})
	id, e := randomIdentifier()
	if e != nil {
		return StorageFailure
	}
	temp := "." + installation + "." + id + ".tmp"
	fd, e := unix.Openat(dir, temp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if e != nil {
		return StorageFailure
	}
	var staged unix.Stat_t
	cleanup := true
	defer func() {
		unix.Close(fd)
		if cleanup && sameEntry(dir, temp, &staged) {
			unix.Unlinkat(dir, temp, 0)
		}
	}()
	if unix.Fchmod(fd, 0600) != nil || unix.Fstat(fd, &staged) != nil || !safeFile(&staged, s.owner) {
		return UnsafeState
	}
	for off := 0; off < len(replacement); {
		n, err := unix.Write(fd, replacement[off:])
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil || n <= 0 {
			return StorageFailure
		}
		off += n
	}
	if unix.Fsync(fd) != nil || unix.Close(fd) != nil {
		fd = -1
		return StorageFailure
	}
	fd = -1
	current, currentStat, e := readPermitAt(dir, name, s.owner)
	if e != nil || !bytes.Equal(current, data) || currentStat.Dev != original.Dev || currentStat.Ino != original.Ino {
		return Conflict
	}
	var currentDirectory unix.Stat_t
	if unix.Fstat(dir, &currentDirectory) != nil || currentDirectory.Dev != directoryStat.Dev || currentDirectory.Ino != directoryStat.Ino {
		return Conflict
	}
	if e = unix.Renameat(dir, temp, dir, name); e != nil {
		return StorageFailure
	}
	cleanup = false
	if unix.Fsync(dir) != nil {
		return PublicationUncertain
	}
	return nil
}
