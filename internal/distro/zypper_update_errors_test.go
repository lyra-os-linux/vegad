package distro

import (
	"errors"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

func TestUpdateAllPreservesLateRepositoryLock(t *testing.T) {
	first := exec.Command("/bin/sh", "-c", "exit 4").Run()
	locked := exec.Command("/bin/sh", "-c", "exit 7").Run()
	listed := false
	var aliases []string
	err := updateAllByRepository(func() ([]RepoUpdateGroup, error) {
		listed = true
		return []RepoUpdateGroup{
			{Alias: "one", DisplayName: "First", Packages: []PackageRef{{Id: "a"}}},
			{Alias: "two", DisplayName: "Second", Packages: []PackageRef{{Id: "b"}}},
		}, nil
	}, func(args []string, _ ProgressFunc, _ PackageProgressFunc, _, _ string) error {
		if !listed {
			t.Fatal("transaction before listing")
		}
		alias := args[len(args)-1]
		aliases = append(aliases, alias)
		if alias == "one" {
			return first
		}
		return locked // lock appears after successful listing
	}, func(uint32, string) {}, nil)
	if !reflect.DeepEqual(aliases, []string{"one", "two"}) {
		t.Fatalf("transactions: %v", aliases)
	}
	if !errors.Is(err, first) || !errors.Is(err, locked) {
		t.Fatalf("lost subprocess causes: %v", err)
	}
	if !strings.Contains(err.Error(), "First: exit status 4; Second: exit status 7") {
		t.Fatalf("lost repository context: %v", err)
	}
	branches := err.(interface{ Unwrap() []error }).Unwrap()
	var exitErr *exec.ExitError
	if !errors.As(branches[1], &exitErr) || exitErr.ExitCode() != 7 {
		t.Fatal("lock cause not reachable")
	}
}
func TestUpdateAllListingFailureStartsNoTransaction(t *testing.T) {
	failure := errors.New("listing failed")
	err := updateAllByRepository(func() ([]RepoUpdateGroup, error) { return nil, failure }, func([]string, ProgressFunc, PackageProgressFunc, string, string) error {
		t.Fatal("transaction after listing failure")
		return nil
	}, func(uint32, string) {}, nil)
	if err != failure {
		t.Fatal(err)
	}
}
