package parental

import (
	"errors"
	"golang.org/x/sys/unix"
)

// directoryLease owns independent descriptors and never creates state. Callers
// must trust path ancestry and cooperating writers must use the same lock inode.
type directoryLease struct {
	dir, lock int
	name      string
	owner     uint32
	stat      unix.Stat_t
	unsafe    Code
}

func openLease(path, name string, owner uint32, exclusive bool, unsafe Code) (lease *directoryLease, err error) {
	d, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fileError(err, unsafe)
	}
	l := &directoryLease{dir: d, lock: -1, name: name, owner: owner, unsafe: unsafe}
	defer func() {
		if err != nil {
			l.close()
		}
	}()
	var ds unix.Stat_t
	if unix.Fstat(d, &ds) != nil || ds.Uid != owner || ds.Mode&0077 != 0 {
		return nil, unsafe
	}
	l.lock, err = unix.Openat(d, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, fileError(err, unsafe)
	}
	if unix.Fstat(l.lock, &l.stat) != nil || !safeFile(&l.stat, owner) {
		return nil, unsafe
	}
	mode := unix.LOCK_SH | unix.LOCK_NB
	if exclusive {
		mode = unix.LOCK_EX | unix.LOCK_NB
	}
	if err = unix.Flock(l.lock, mode); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) {
			return nil, Busy
		}
		return nil, unsafe
	}
	if err = l.stable(); err != nil {
		return nil, err
	}
	return l, nil
}
func fileError(err error, unsafe Code) error {
	if errors.Is(err, unix.ENOENT) {
		return RecoveryRequired
	}
	return unsafe
}
func (l *directoryLease) stable() error {
	var ds unix.Stat_t
	if unix.Fstat(l.dir, &ds) != nil || ds.Uid != l.owner || ds.Mode&0077 != 0 || !sameEntry(l.dir, l.name, &l.stat) {
		return l.unsafe
	}
	return nil
}
func (l *directoryLease) close() error {
	failed := false
	if l.lock >= 0 {
		failed = unix.Close(l.lock) != nil
		l.lock = -1
	}
	if l.dir >= 0 {
		failed = unix.Close(l.dir) != nil || failed
		l.dir = -1
	}
	if failed {
		return l.unsafe
	}
	return nil
}
func (l *directoryLease) read(name string, limit int) (data []byte, err error) {
	fd, err := unix.Openat(l.dir, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, fileError(err, l.unsafe)
	}
	defer func() {
		if unix.Close(fd) != nil && err == nil {
			data = nil
			err = l.unsafe
		}
	}()
	var before unix.Stat_t
	if unix.Fstat(fd, &before) != nil || !safeFile(&before, l.owner) || before.Size <= 0 || before.Size > int64(limit) {
		return nil, l.unsafe
	}
	data = make([]byte, 0, int(before.Size))
	buf := make([]byte, 4096)
	for len(data) <= limit {
		n, e := unix.Read(fd, buf[:min(len(buf), limit+1-len(data))])
		if errors.Is(e, unix.EINTR) {
			continue
		}
		if e != nil {
			return nil, l.unsafe
		}
		if n == 0 {
			break
		}
		data = append(data, buf[:n]...)
	}
	var after unix.Stat_t
	if len(data) != int(before.Size) || unix.Fstat(fd, &after) != nil || !safeFile(&after, l.owner) || after.Size != before.Size || after.Mtim != before.Mtim || after.Ctim != before.Ctim || !sameEntry(l.dir, name, &before) {
		return nil, l.unsafe
	}
	return data, nil
}
