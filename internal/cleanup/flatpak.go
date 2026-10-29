package cleanup

import (
	"bufio"
	"bytes"
	"context"
	"path/filepath"
	"regexp"
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
	return parseFlatpakUnused(out), nil
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
	seen := map[[2]uint64]bool{}
	for _, ref := range refs {
		m.items = append(m.items, ref.id+"//"+ref.branch)
		deploys, _ := filepath.Glob(filepath.Join(env.path(flatpakSystemDir), "runtime", ref.id, "*", ref.branch))
		for _, deploy := range deploys {
			size, _, err := treeSize(deploy, seen)
			if err == nil {
				m.bytes += size
			}
		}
	}
	return m, nil
}

func cleanFlatpak(ctx context.Context, env *Env, _ measurement) error {
	_, err := runOK(ctx, env, "", "flatpak", "uninstall", "--unused", "--system", "--noninteractive", "-y")
	return err
}
