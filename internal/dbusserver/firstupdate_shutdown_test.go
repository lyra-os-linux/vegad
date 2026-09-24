package dbusserver

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/lyraos/vegad/internal/distro"
	"github.com/lyraos/vegad/internal/profile"
)

type stoppingPreparer struct{ step func(string) }

func (p stoppingPreparer) SyncDatabase() error                       { p.step("refresh"); return nil }
func (p stoppingPreparer) ListUpdates() ([]distro.PackageRef, error) { p.step("list"); return nil, nil }
func TestPreparationCancellationNeverCompletesOrStartsNextStage(t *testing.T) {
	phases := []string{"import", "refresh", "list", "publish"}
	for index, phase := range phases {
		t.Run(phase, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			marker := filepath.Join(t.TempDir(), "done")
			var calls []string
			step := func(name string) {
				calls = append(calls, name)
				if name == phase {
					cancel()
				}
			}
			err := prepareInitialRepositoriesContext(ctx, profile.Desktop, marker, stoppingPreparer{step}, func() error { step("import"); return nil }, func(UpdateStatus) error { step("publish"); return nil })
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("not cancelled: %v", err)
			}
			if !reflect.DeepEqual(calls, phases[:index+1]) {
				t.Fatalf("work after stop: %v", calls)
			}
			if _, err := os.Stat(marker); !os.IsNotExist(err) {
				t.Fatal("interrupted preparation marked complete")
			}
			status, err := readPreparationStatus(preparationStatePath(marker))
			if err != nil || status.ErrorKind != "interrupted" || status.State != "failed" {
				t.Fatalf("state: %+v %v", status, err)
			}
			// Same files, next invocation: no manual marker removal is needed.
			if err := prepareInitialRepositories(profile.Desktop, marker, stoppingPreparer{func(string) {}}, func() error { return nil }, func(UpdateStatus) error { return nil }); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(marker); err != nil {
				t.Fatal("retry did not complete")
			}
		})
	}
}
