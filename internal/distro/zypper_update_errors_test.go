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

func TestUpdateAllFinishesHeldBackUpdatesAcrossRepositories(t *testing.T) {
	// a-app 2 needs lyratest-lib 2 from z-lib: the a-app pass holds it back,
	// so it is still listed after both repository passes.
	listings := [][]RepoUpdateGroup{
		{
			{Alias: "a-app", DisplayName: "a-app", Packages: []PackageRef{{Id: "lyratest-app"}}},
			{Alias: "z-lib", DisplayName: "z-lib", Packages: []PackageRef{{Id: "lyratest-lib"}}},
		},
		{{Alias: "a-app", DisplayName: "a-app", Packages: []PackageRef{{Id: "lyratest-app"}}}},
	}
	var runs [][]string
	err := updateAllByRepository(func() ([]RepoUpdateGroup, error) {
		next := listings[0]
		if len(listings) > 1 {
			listings = listings[1:]
		}
		return next, nil
	}, func(args []string, _ ProgressFunc, _ PackageProgressFunc, _, _ string) error {
		runs = append(runs, args)
		return nil
	}, func(uint32, string) {}, nil)
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"--non-interactive", "update", "-y", "--repo", "a-app"},
		{"--non-interactive", "update", "-y", "--repo", "z-lib"},
		{"--non-interactive", "update", "-y"},
	}
	if !reflect.DeepEqual(runs, want) {
		t.Fatalf("transactions: %v", runs)
	}
}

func TestUpdateAllSkipsFinalPassWhenNothingIsLeft(t *testing.T) {
	calls := 0
	var runs [][]string
	err := updateAllByRepository(func() ([]RepoUpdateGroup, error) {
		calls++
		if calls > 1 {
			return nil, nil
		}
		return []RepoUpdateGroup{{Alias: "one", DisplayName: "One", Packages: []PackageRef{{Id: "a"}}}}, nil
	}, func(args []string, _ ProgressFunc, _ PackageProgressFunc, _, _ string) error {
		runs = append(runs, args)
		return nil
	}, func(uint32, string) {}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 {
		t.Fatalf("transactions: %v", runs)
	}
}
