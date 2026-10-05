package parental

import (
	"golang.org/x/sys/unix"
)

// Reader only opens the canonical files; it never creates locks, directories,
// records or adopts a temporary file. Ancestors of directory must be trusted.
// Writers must lock the same pre-existing inode exclusively, fsync their new
// file, rename under that lease, and fsync the directory before releasing it.
type Reader struct {
	directory string
	owner     uint32
}

func NewReader(directory string) *Reader { return &Reader{directory: directory, owner: 0} }
func safeFile(s *unix.Stat_t, owner uint32) bool {
	return s.Mode&unix.S_IFMT == unix.S_IFREG && s.Uid == owner && s.Mode&07777 == 0600 && s.Nlink == 1
}
func sameEntry(dir int, name string, s *unix.Stat_t) bool {
	var entry unix.Stat_t
	return unix.Fstatat(dir, name, &entry, unix.AT_SYMLINK_NOFOLLOW) == nil && entry.Dev == s.Dev && entry.Ino == s.Ino && entry.Mode == s.Mode && entry.Uid == s.Uid && entry.Nlink == s.Nlink
}
func (r *Reader) Snapshot() (catalog Catalog, err error) {
	if r == nil {
		return Catalog{}, UnsafeCatalog
	}
	lease, err := openLease(r.directory, "catalog.lock", r.owner, false, UnsafeCatalog)
	if err != nil {
		return Catalog{}, err
	}
	defer func() {
		if closeErr := lease.close(); closeErr != nil && err == nil {
			catalog = Catalog{}
			err = closeErr
		}
	}()
	data, err := lease.read("catalog.json", CatalogLimit)
	if err != nil {
		return Catalog{}, err
	}
	catalog, err = DecodeCatalog(data)
	if err != nil {
		return Catalog{}, err
	}
	if err = lease.stable(); err != nil {
		return Catalog{}, err
	}
	return catalog, nil
}
