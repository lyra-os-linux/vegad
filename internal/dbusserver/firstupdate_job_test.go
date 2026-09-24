package dbusserver

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"

	"github.com/lyraos/vegad/internal/distro"
	"github.com/lyraos/vegad/internal/profile"
)

type jobTestBackend struct{ step func(string) error }

func (b jobTestBackend) SyncDatabase() error { return b.step("refresh") }
func (b jobTestBackend) ListUpdates() ([]distro.PackageRef, error) {
	if err := b.step("list"); err != nil {
		return nil, err
	}
	return []distro.PackageRef{{Id: "vim"}, {Id: "kernel-default"}}, nil
}
func (b jobTestBackend) DiscoverPreparationKey() error {
	_ = b.step("discover")
	return &distro.UntrustedKeyError{Repo: "fixture", Fingerprint: "0123456789ABCDEF0123456789ABCDEF01234567"}
}

func TestFirstUpdateJobFailureThenRetry(t *testing.T) {
	for _, scenario := range []string{"detect", "import", "refresh", "key", "list", "publish", "initial-state", "marker", "completed-state"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			marker := filepath.Join(root, "done")
			updatesPath := filepath.Join(root, "updates.json")
			previous := UpdateStatus{Profile: "desktop", NativeCount: 9, FlatpakCount: 3, TotalCount: 12}
			if err := persistUpdateStatus(updatesPath, previous); err != nil {
				t.Fatal(err)
			}
			failing := true
			failure := syscall.ENOSPC
			var calls []string
			step := func(name string) error {
				calls = append(calls, name)
				if failing && scenario == name {
					return failure
				}
				if failing && scenario == "key" && name == "refresh" {
					return &distro.UntrustedKeyError{Fingerprint: "0123456789ABCDEF0123456789ABCDEF01234567"}
				}
				return nil
			}
			job := firstUpdateJob{
				marker:      marker,
				readCmdline: func() ([]byte, error) { return []byte("root=UUID=installed"), nil },
				openBackend: func(context.Context) (repositoryPreparer, error) {
					if err := step("detect"); err != nil {
						return nil, err
					}
					return jobTestBackend{step}, nil
				},
				importKeys: func(context.Context) error { return step("import") },
				publish: func(status UpdateStatus) error {
					if _, err := os.Stat(marker); !os.IsNotExist(err) {
						t.Fatal("completion before publication")
					}
					if err := step("publish"); err != nil {
						return err
					}
					if status.NativeCount != 2 || status.FlatpakCount != 3 || status.TotalCount != 5 {
						t.Fatalf("counts: %+v", status)
					}
					return persistUpdateStatus(updatesPath, status)
				},
				storage: preparationPersistence{
					status: func(path string, status PreparationStatus) error {
						if failing && ((scenario == "initial-state" && status.State == "running" && status.Phase == "importing-keys") || (scenario == "completed-state" && status.State == "completed")) {
							return failure
						}
						return persistPreparationStatus(path, status)
					},
					marker: func(path string) error {
						if failing && scenario == "marker" {
							return failure
						}
						return writeFirstUpdateMarker(path)
					},
					previous: func() (UpdateStatus, error) { return readUpdateStatus(updatesPath) },
				},
			}
			err := job.run(context.Background(), profile.Desktop)
			if scenario == "key" {
				var key *distro.UntrustedKeyError
				if !errors.As(err, &key) || key.Repo != "fixture" {
					t.Fatalf("key proposal lost: %v", err)
				}
			} else if !errors.Is(err, failure) {
				t.Fatalf("failure lost: %v", err)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatalf("failed job marked complete (%s)", scenario)
			}
			status, err := readPreparationStatus(preparationStatePath(marker))
			if err != nil {
				t.Fatal(err)
			}
			want := "failed"
			if scenario == "key" {
				want = "awaiting-approval"
			}
			if status.State != want {
				t.Fatalf("state: %+v", status)
			}
			if scenario == "detect" || scenario == "initial-state" {
				if !reflect.DeepEqual(calls, []string{"detect"}) {
					t.Fatalf("work after early failure: %v", calls)
				}
			}
			// Same persistent directory, new backend instance: a real retry must execute
			// all phases, publish the correct counts and only then mark completion.
			failing = false
			calls = nil
			if err := job.run(context.Background(), profile.Desktop); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(calls, []string{"detect", "import", "refresh", "list", "publish"}) {
				t.Fatalf("retry order: %v", calls)
			}
			if _, err := os.Stat(marker); err != nil {
				t.Fatal("retry did not complete")
			}
			status, err = readPreparationStatus(preparationStatePath(marker))
			if err != nil || status.State != "completed" {
				t.Fatalf("retry status: %+v %v", status, err)
			}
			calls = nil
			if err := job.run(context.Background(), profile.Desktop); err != nil || len(calls) != 0 {
				t.Fatalf("completed job repeated: %v %v", calls, err)
			}
		})
	}
}

func TestFirstUpdateJobEligibilitySkipsAllAdministrativeWork(t *testing.T) {
	for _, scenario := range []string{"live-flag", "live-root", "completed", "legacy"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			marker := filepath.Join(root, "done")
			cmdline := "root=UUID=installed"
			switch scenario {
			case "live-flag":
				cmdline += " rd.live.image"
			case "live-root":
				cmdline = "root=live:CDLABEL=LyraOS"
			case "completed":
				if err := writeFirstUpdateMarker(marker); err != nil {
					t.Fatal(err)
				}
			case "legacy":
				if err := os.WriteFile(filepath.Join(root, "first-update.skipped"), nil, 0644); err != nil {
					t.Fatal(err)
				}
			}
			// Nil dependencies deliberately panic if an excluded job reaches any work.
			job := firstUpdateJob{marker: marker, readCmdline: func() ([]byte, error) { return []byte(cmdline), nil }}
			if err := job.run(context.Background(), profile.Desktop); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(preparationStatePath(marker)); !os.IsNotExist(err) {
				t.Fatal("excluded job persisted state")
			}
		})
	}
}

func TestCompletionMarkerCleansFailedAtomicPublication(t *testing.T) {
	root := t.TempDir()
	destination := filepath.Join(root, "blocked")
	if err := os.Mkdir(destination, 0755); err != nil {
		t.Fatal(err)
	}
	if err := writeFirstUpdateMarker(destination); err == nil {
		t.Fatal("expected publication failure")
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 || entries[0].Name() != "blocked" {
		t.Fatalf("temporary marker leaked: %v %v", entries, err)
	}
}
