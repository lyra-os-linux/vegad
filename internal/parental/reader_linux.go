package parental

import (
	"errors"

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
func storageError(err error) error {
	if errors.Is(err, unix.ENOENT) {
		return RecoveryRequired
	}
	return UnsafeCatalog
}
func safeFile(s *unix.Stat_t, owner uint32) bool {
	return s.Mode&unix.S_IFMT == unix.S_IFREG && s.Uid == owner && s.Mode&07777 == 0600 && s.Nlink == 1
}
func sameEntry(dir int, name string, s *unix.Stat_t) bool {
	var entry unix.Stat_t
	return unix.Fstatat(dir, name, &entry, unix.AT_SYMLINK_NOFOLLOW) == nil && entry.Dev == s.Dev && entry.Ino == s.Ino && entry.Mode == s.Mode && entry.Uid == s.Uid && entry.Nlink == s.Nlink
}
func (r *Reader) Snapshot() (Catalog, error) {
	if r == nil {
		return Catalog{}, UnsafeCatalog
	}
	dfd, err := unix.Open(r.directory, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return Catalog{}, storageError(err)
	}
	defer unix.Close(dfd)
	var ds unix.Stat_t
	if unix.Fstat(dfd, &ds) != nil || ds.Uid != r.owner || ds.Mode&0077 != 0 {
		return Catalog{}, UnsafeCatalog
	}
	lock, err := unix.Openat(dfd, "catalog.lock", unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return Catalog{}, storageError(err)
	}
	defer unix.Close(lock)
	var ls unix.Stat_t
	if unix.Fstat(lock, &ls) != nil || !safeFile(&ls, r.owner) {
		return Catalog{}, UnsafeCatalog
	}
	if err = unix.Flock(lock, unix.LOCK_SH|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return Catalog{}, Busy
		}
		return Catalog{}, UnsafeCatalog
	}
	if !sameEntry(dfd, "catalog.lock", &ls) {
		return Catalog{}, UnsafeCatalog
	}
	fd, err := unix.Openat(dfd, "catalog.json", unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return Catalog{}, storageError(err)
	}
	defer unix.Close(fd)
	var fs unix.Stat_t
	if unix.Fstat(fd, &fs) != nil || !safeFile(&fs, r.owner) || fs.Size <= 0 || fs.Size > CatalogLimit {
		return Catalog{}, UnsafeCatalog
	}
	data := make([]byte, 0, int(fs.Size))
	buf := make([]byte, 4096)
	for len(data) <= CatalogLimit {
		count, err := unix.Read(fd, buf[:min(len(buf), CatalogLimit+1-len(data))])
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return Catalog{}, UnsafeCatalog
		}
		if count == 0 {
			break
		}
		data = append(data, buf[:count]...)
	}
	var after unix.Stat_t
	if len(data) != int(fs.Size) || unix.Fstat(fd, &after) != nil || !safeFile(&after, r.owner) || after.Size != fs.Size || after.Mtim != fs.Mtim || after.Ctim != fs.Ctim || !sameEntry(dfd, "catalog.json", &fs) || !sameEntry(dfd, "catalog.lock", &ls) {
		return Catalog{}, UnsafeCatalog
	}
	return DecodeCatalog(data)
}
