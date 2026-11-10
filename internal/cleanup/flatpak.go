package cleanup

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
)

const flatpakSystemDir = "/var/lib/flatpak"

// A row of the confirmation table "flatpak uninstall --unused" prints before
// asking: " 1.\t   \torg.gtk.Gtk3theme.Adwaita-dark\t3.22\tr".
var flatpakUnusedRow = regexp.MustCompile(`^\s*\d+\.\s+([A-Za-z0-9_.-]+)\s+([A-Za-z0-9_.-]+)\s+r\s*$`)

type flatpakRef struct{ id, branch string }

func parseFlatpakUnused(out []byte) []flatpakRef {
	var refs []flatpakRef
	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		if match := flatpakUnusedRow.FindStringSubmatch(scanner.Text()); match != nil {
			refs = append(refs, flatpakRef{match[1], match[2]})
		}
	}
	return refs
}

// unusedSystemRuntimes asks Flatpak itself which runtimes it would remove and
// answers "n": the list is exactly the one "flatpak uninstall --unused"
// applies, pinned runtimes and extensions included.
func unusedSystemRuntimes(ctx context.Context, env *Env) ([]flatpakRef, error) {
	// Exit code 1 means the question was declined; only the table matters.
	out, _, err := env.Run(ctx, "n\n", "flatpak", "uninstall", "--unused", "--system")
	if err != nil {
		return nil, err
	}
	refs := parseFlatpakUnused(out)
	// An empty list only counts when Flatpak said there is nothing; output we
	// do not recognize fails the measurement instead of reporting "nothing".
	if len(refs) == 0 && !bytes.Contains(out, []byte("Nothing unused")) {
		return nil, errors.New("unrecognized flatpak uninstall --unused output")
	}
	return refs, nil
}

func measureFlatpak(ctx context.Context, env *Env) (measurement, error) {
	if reason := missingTool(env, "flatpak", "flatpak"); reason != "" {
		return measurement{reason: reason}, nil
	}
	refs, err := unusedSystemRuntimes(ctx, env)
	if err != nil {
		return measurement{}, err
	}
	m := measurement{count: len(refs)}
	var deploys []string
	for _, ref := range refs {
		m.items = append(m.items, ref.id+"//"+ref.branch)
		found, _ := filepath.Glob(filepath.Join(env.path(flatpakSystemDir), "runtime", ref.id, "*", ref.branch))
		deploys = append(deploys, found...)
	}
	m.bytes = exclusiveSize(deploys)
	return m, nil
}

// exclusiveSize is the space removing these Flatpak deploys would free. ostree
// keeps each file once in the repository and hard-links it into every deploy
// that uses it, so a file only counts when all of its links but the
// repository's are inside these deploys: what is shared with runtimes that
// stay is not freed. It is still an estimate (vega#242).
func exclusiveSize(roots []string) uint64 {
	type inode struct{ found, nlink, size uint64 }
	seen := map[[2]uint64]*inode{}
	for _, root := range roots {
		rootInfo, err := os.Lstat(root)
		if err != nil || !rootInfo.IsDir() {
			continue
		}
		rootStat, ok := rootInfo.Sys().(*syscall.Stat_t)
		if !ok {
			continue
		}
		_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return nil
			}
			st, ok := info.Sys().(*syscall.Stat_t)
			if !ok || st.Dev != rootStat.Dev {
				if info.IsDir() && path != root {
					return filepath.SkipDir
				}
				return nil
			}
			if !info.Mode().IsRegular() {
				return nil
			}
			key := [2]uint64{uint64(st.Dev), uint64(st.Ino)}
			entry := seen[key]
			if entry == nil {
				entry = &inode{nlink: uint64(st.Nlink), size: allocated(info)}
				seen[key] = entry
			}
			entry.found++
			return nil
		})
	}
	var total uint64
	for _, entry := range seen {
		if entry.nlink <= 1 || entry.found >= entry.nlink-1 {
			total += entry.size
		}
	}
	return total
}

func cleanFlatpak(ctx context.Context, env *Env, _ measurement) error {
	_, err := runOK(ctx, env, "", "flatpak", "uninstall", "--unused", "--system", "--noninteractive", "-y")
	return err
}
