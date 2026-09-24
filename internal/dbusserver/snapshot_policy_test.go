package dbusserver

import (
	"bytes"
	"errors"
	"github.com/lyraos/vegad/internal/profile"
	"log"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSnapshotPolicyBestEffortAndHonestLogs(t *testing.T) {
	failure := errors.New("snapshot creation failed")
	workFailure := errors.New("package operation failed")
	for _, tc := range []struct {
		name            string
		pre, post, work error
		calls           []string
	}{
		{"success", nil, nil, nil, []string{"pre", "work", "post"}},
		{"unavailable pre", errSnapperUnavailable, nil, nil, []string{"pre", "work"}},
		{"failed pre", failure, nil, nil, []string{"pre", "work"}},
		{"failed work", nil, nil, workFailure, []string{"pre", "work", "post"}},
		{"failed pre and work", failure, nil, workFailure, []string{"pre", "work"}},
		{"failed post", nil, failure, nil, []string{"pre", "work", "post"}},
		{"unavailable post", nil, errSnapperUnavailable, nil, []string{"pre", "work", "post"}},
		{"failed post and work", nil, failure, workFailure, []string{"pre", "work", "post"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls []string
			var output bytes.Buffer
			got := withSnapshotPolicy("fixture", func() error { calls = append(calls, "work"); return tc.work }, func(kind, description string, pre ...uint32) (uint32, error) {
				calls = append(calls, kind)
				if description != "fixture" {
					t.Fatal("lost description")
				}
				if kind == "pre" {
					if len(pre) != 0 {
						t.Fatal("unexpected pre link")
					}
					return 42, tc.pre
				}
				if !reflect.DeepEqual(pre, []uint32{42}) {
					t.Fatalf("post not linked: %v", pre)
				}
				return 43, tc.post
			}, log.New(&output, "", 0).Printf)
			if got != tc.work {
				t.Fatalf("snapshot result replaced operation result: %v", got)
			}
			if !reflect.DeepEqual(calls, tc.calls) {
				t.Fatalf("calls: %v", calls)
			}
			text := output.String()
			if strings.Contains(text, "snapshot pre criado") != (tc.pre == nil) {
				t.Fatalf("false pre claim: %s", text)
			}
			if strings.Contains(text, "snapshot post criado") != (tc.pre == nil && tc.post == nil) {
				t.Fatalf("false post claim: %s", text)
			}
			if tc.pre != nil && !strings.Contains(text, "continuará sem snapshot pre") {
				t.Fatalf("missing policy: %s", text)
			}
			if tc.pre == nil && tc.post != nil && !strings.Contains(text, "par pre/post incompleto (pre 42)") {
				t.Fatalf("missing incomplete pair: %s", text)
			}
		})
	}
}

func TestInitialPreparationDoesNotAttemptSnapshots(t *testing.T) {
	root := t.TempDir()
	attempted := filepath.Join(root, "snapshot-attempted")
	t.Setenv("SNAPSHOT_TRIPWIRE", attempted)
	t.Setenv("PATH", root)
	t.Setenv("VEGAD_UPDATE_STATE", filepath.Join(root, "updates.json"))
	if err := os.WriteFile(filepath.Join(root, "snapper"), []byte("#!/bin/sh\nprintf attempted > \"$SNAPSHOT_TRIPWIRE\"\nexit 1\n"), 0755); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "done")
	var calls []string
	err := prepareInitialRepositories(profile.Desktop, marker, preparationBackend{calls: &calls}, func() error { return nil }, func(UpdateStatus) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("preparation did not complete")
	}
	if _, err := os.Stat(attempted); !os.IsNotExist(err) {
		t.Fatal("preparation attempted a snapshot")
	}
}
