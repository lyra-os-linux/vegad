package cleanup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type call struct {
	stdin string
	line  string
}

type fakeHost struct {
	t       *testing.T
	env     *Env
	replies map[string]reply
	calls   []call
	tools   map[string]bool
	snaps   int
}

type reply struct {
	out  string
	code int
	// effect runs when the command is called (e.g. zypper clean empties the cache).
	effect func()
}

func newHost(t *testing.T) *fakeHost {
	t.Helper()
	h := &fakeHost{t: t, replies: map[string]reply{}, tools: map[string]bool{
		"zypper": true, "journalctl": true, "flatpak": true, "snapper": true,
	}}
	h.env = &Env{
		Root: t.TempDir(),
		Run: func(_ context.Context, stdin, name string, args ...string) ([]byte, int, error) {
			line := strings.Join(append([]string{name}, args...), " ")
			h.calls = append(h.calls, call{stdin, line})
			r, ok := h.replies[line]
			if !ok {
				return nil, 127, nil
			}
			if r.effect != nil {
				r.effect()
			}
			return []byte(r.out), r.code, nil
		},
		LookPath: func(tool string) (string, error) {
			if h.tools[tool] {
				return "/usr/bin/" + tool, nil
			}
			return "", errors.New("not found")
		},
		Snapshot: func(_ string, work func() error) error {
			h.snaps++
			return work()
		},
		Now: func() time.Time { return time.Date(2026, 10, 29, 21, 0, 0, 0, time.Local) },
	}
	return h
}

func (h *fakeHost) write(path string, size int) string {
	h.t.Helper()
	full := h.env.path(path)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(full, make([]byte, size), 0o644); err != nil {
		h.t.Fatal(err)
	}
	return full
}

func (h *fakeHost) ran(line string) bool {
	for _, c := range h.calls {
		if c.line == line {
			return true
		}
	}
	return false
}

func fixture(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func categoryByID(t *testing.T, rows []Category, id string) Category {
	t.Helper()
	for _, row := range rows {
		if row.ID == id {
			return row
		}
	}
	t.Fatalf("categoria %s ausente", id)
	return Category{}
}

func TestScanReportsEveryCategoryInOrderAndDeletesNothing(t *testing.T) {
	h := newHost(t)
	dump := h.write(coredumpDir+"/core.app.1000.zst", 8192)
	rows := Scan(context.Background(), h.env)
	if len(rows) != len(IDs) {
		t.Fatalf("%d categorias", len(rows))
	}
	for i, id := range IDs {
		if rows[i].ID != id {
			t.Fatalf("ordem: %v", rows)
		}
	}
	if _, err := os.Stat(dump); err != nil {
		t.Fatal("Scan apagou um core dump")
	}
	core := categoryByID(t, rows, "coredumps")
	if !core.CanClean || core.Bytes == 0 || core.Detail != "count=1" {
		t.Fatalf("coredumps: %+v", core)
	}
}

func TestEmptyCoredumpsCannotBeCleaned(t *testing.T) {
	h := newHost(t)
	m, err := measureCoredumps(context.Background(), h.env)
	if err != nil {
		t.Fatal(err)
	}
	row := m.category("coredumps")
	if row.CanClean || row.Bytes != 0 || row.Detail != "count=0;reason=nothing" {
		t.Fatalf("%+v", row)
	}
}

func TestCleanCoredumpsRemovesFilesButNeverFollowsLinks(t *testing.T) {
	h := newHost(t)
	h.write(coredumpDir+"/core.a.zst", 4096)
	h.write(coredumpDir+"/core.b.zst", 4096)
	precious := h.write("/home/user/Documentos/tese.odt", 100)
	if err := os.Symlink(filepath.Dir(precious), h.env.path(coredumpDir+"/link")); err != nil {
		t.Fatal(err)
	}
	freed, result, err := Clean(context.Background(), h.env, "coredumps")
	if err != nil || result != ResultOK || freed < 8192 {
		t.Fatalf("freed=%d result=%s err=%v", freed, result, err)
	}
	if _, err := os.Stat(precious); err != nil {
		t.Fatal("o link levou a um arquivo pessoal")
	}
	if _, err := os.Lstat(h.env.path(coredumpDir + "/link")); err != nil {
		t.Fatal("o link não é um core dump e deve ficar")
	}
	if _, err := os.Stat(h.env.path(coredumpDir + "/core.a.zst")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("core dump ficou")
	}
}

func TestCleanSkipsAnEmptyCategoryAndRejectsUnknownIDs(t *testing.T) {
	h := newHost(t)
	if _, result, err := Clean(context.Background(), h.env, "coredumps"); result != ResultSkipped || err != nil {
		t.Fatalf("%s %v", result, err)
	}
	if _, _, err := Clean(context.Background(), h.env, "home"); err == nil || Known("home") {
		t.Fatal("categoria desconhecida aceita")
	}
}

func TestPackageCacheUsesZypperCleanAndReportsFreedBytes(t *testing.T) {
	h := newHost(t)
	rpm := h.write(packageCacheDir+"/repo-oss/x86_64/big.rpm", 64<<10)
	h.replies["zypper -n clean"] = reply{effect: func() { _ = os.Remove(rpm) }}
	freed, result, err := Clean(context.Background(), h.env, "package-cache")
	if err != nil || result != ResultOK || freed < 64<<10 {
		t.Fatalf("freed=%d result=%s err=%v", freed, result, err)
	}
	if !h.ran("zypper -n clean") || h.ran("zypper -n clean --all") {
		t.Fatalf("%v", h.calls)
	}
}

func TestMissingToolBlocksTheCategory(t *testing.T) {
	h := newHost(t)
	h.tools["snapper"] = false
	row := categoryByID(t, Scan(context.Background(), h.env), "snapshots")
	if row.CanClean || row.Detail != "reason=missing-tool:snapper" {
		t.Fatalf("%+v", row)
	}
}

func TestDetailCapsItemsAndEscapesSeparators(t *testing.T) {
	m := measurement{count: 25}
	for i := 0; i < 25; i++ {
		m.items = append(m.items, "a;b")
	}
	detail := m.category("x").Detail
	if strings.Count(detail, "item=") != maxItems || strings.Contains(detail, "a;b") {
		t.Fatal(detail)
	}
}
