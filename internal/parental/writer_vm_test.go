package parental

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

const writerVMRoot = "/root/lyra-go-writer-test/fixtures"

func writerVMGuard(t *testing.T) {
	t.Helper()
	cmd, e := os.ReadFile("/proc/cmdline")
	serial, e2 := os.ReadFile("/sys/block/vda/serial")
	if os.Geteuid() != 0 || e != nil || e2 != nil || !strings.Contains(string(cmd), "lyra.parental-identity-test=1") || strings.TrimSpace(string(serial)) != "lyra-admission-test" {
		t.Fatal("wrong guest/disk/identity")
	}
}
func TestCatalogWriterVMChild(t *testing.T) {
	if os.Getenv("LYRA_PARENTAL_WRITER_VM_CHILD") != "1" {
		t.Skip("root VM child only")
	}
	writerVMGuard(t)
	dir := os.Getenv("LYRA_PARENTAL_WRITER_VM_DIR")
	relative, e := filepath.Rel(writerVMRoot, dir)
	if e != nil || relative == "." || strings.HasPrefix(relative, "..") || filepath.IsAbs(relative) {
		t.Fatal("wrong fixture")
	}
	expectedData, e := os.ReadFile(filepath.Join(dir, "expected.json"))
	if e != nil {
		t.Fatal(e)
	}
	expected, e := DecodeCatalog(expectedData)
	if e != nil {
		t.Fatal(e)
	}
	writer := NewCatalogWriter(dir)
	input := bufio.NewScanner(os.Stdin)
	emit := func(v map[string]interface{}) { data, _ := json.Marshal(v); fmt.Println(string(data)) }
	emit(map[string]interface{}{"stage": "ready", "expected": expected.Digest(), "pid": os.Getpid()})
	if !input.Scan() || input.Text() != "go" {
		t.Fatal("missing start")
	}
	mode := os.Getenv("LYRA_PARENTAL_WRITER_VM_MODE")
	writer.checkpoint = func(stage string) {
		if mode == "hold" && stage == "file-synced" {
			emit(map[string]interface{}{"stage": "held"})
			if !input.Scan() || input.Text() != "release" {
				t.Fatal("missing release")
			}
		}
		if mode == stage {
			os.Exit(19)
		}
	}
	result, e := writer.BlockOrdinary(expected, strings.Repeat("a", 32), strings.Repeat("e", 32))
	code := "Committed"
	if e != nil {
		code = e.Error()
	}
	emit(map[string]interface{}{"stage": "result", "code": code, "digest": result.Digest(), "revision": result.Revision()})
}

type writerVMChild struct {
	cmd    *exec.Cmd
	input  io.WriteCloser
	output *bufio.Scanner
}

func startWriterVMChild(t *testing.T, dir, mode string) *writerVMChild {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestCatalogWriterVMChild$")
	cmd.Env = append(os.Environ(), "LYRA_PARENTAL_WRITER_VM_CHILD=1", "LYRA_PARENTAL_WRITER_VM_DIR="+dir, "LYRA_PARENTAL_WRITER_VM_MODE="+mode)
	stdin, e := cmd.StdinPipe()
	if e != nil {
		t.Fatal(e)
	}
	stdout, e := cmd.StdoutPipe()
	if e != nil {
		t.Fatal(e)
	}
	cmd.Stderr = os.Stderr
	if e = cmd.Start(); e != nil {
		t.Fatal(e)
	}
	child := &writerVMChild{cmd, stdin, bufio.NewScanner(stdout)}
	t.Cleanup(func() {
		stdin.Close()
		if cmd.ProcessState == nil {
			cmd.Process.Kill()
			cmd.Wait()
		}
	})
	return child
}
func (c *writerVMChild) read(t *testing.T) map[string]interface{} {
	t.Helper()
	if !c.output.Scan() {
		t.Fatal("missing child result", c.output.Err())
	}
	var result map[string]interface{}
	if e := json.Unmarshal(c.output.Bytes(), &result); e != nil {
		t.Fatal(e, c.output.Text())
	}
	return result
}
func (c *writerVMChild) send(t *testing.T, line string) {
	t.Helper()
	if _, e := fmt.Fprintln(c.input, line); e != nil {
		t.Fatal(e)
	}
}
func (c *writerVMChild) wait(t *testing.T, code int) {
	t.Helper()
	e := c.cmd.Wait()
	if code == 0 {
		if e != nil {
			t.Fatal(e)
		}
	} else {
		exit, ok := e.(*exec.ExitError)
		if !ok || exit.ExitCode() != code {
			t.Fatal("wrong exit", e)
		}
	}
}

func TestCatalogWriterRootDisposableVM(t *testing.T) {
	if os.Getenv("LYRA_PARENTAL_WRITER_VM") != "1" {
		t.Skip("requires disposable VM/controller")
	}
	writerVMGuard(t)
	if e := os.Mkdir(writerVMRoot, 0700); e != nil {
		t.Fatal(e)
	}
	defer os.RemoveAll(writerVMRoot)
	fixture := func(name string) (string, Catalog) {
		dir := filepath.Join(writerVMRoot, name)
		if e := os.Mkdir(dir, 0700); e != nil {
			t.Fatal(e)
		}
		row := fixtureRow()
		peer := func(assoc, gen string, uid uint32, kind, state, operation string) map[string]interface{} {
			policy := ""
			adapters := map[string]interface{}{}
			if kind == "supervised" {
				policy = strings.Repeat("f", 64)
				adapters["fixture"] = strings.Repeat("d", 64)
			}
			return map[string]interface{}{"association": strings.Repeat(assoc, 32), "generation": strings.Repeat(gen, 32), "identity": Identity{fmt.Sprintf("peer%d", uid), uid, 100, "users"}, "revision": 1, "kind": kind, "state": state, "operation": operation, "barrier": state != "active", "policy": policy, "adapters": adapters}
		}
		row["accounts"] = append(row["accounts"].([]interface{}), peer("c", "d", 1003, "ordinary", "active", ""), peer("f", "1", 1004, "supervised", "active", ""), peer("2", "3", 1005, "supervised", "tombstone", strings.Repeat("4", 32)))
		data := encodeRow(t, row)
		writePrivate(t, filepath.Join(dir, "catalog.lock"), nil)
		writePrivate(t, filepath.Join(dir, "catalog.json"), data)
		writePrivate(t, filepath.Join(dir, "expected.json"), data)
		return dir, decodeRow(t, row)
	}
	metadata := func(dir string) {
		for _, name := range []string{"catalog.json", "catalog.lock"} {
			var stat unix.Stat_t
			if e := unix.Lstat(filepath.Join(dir, name), &stat); e != nil || !safeFile(&stat, 0) {
				t.Fatal("unsafe root metadata", name, e)
			}
		}
	}
	preserved := func(before, after Catalog) {
		if !reflect.DeepEqual(before.Accounts()[1:], after.Accounts()[1:]) {
			t.Fatal("peers changed")
		}
	}
	snapshot := func(dir string) Catalog {
		c, e := NewReader(dir).Snapshot()
		if e != nil {
			t.Fatal(e)
		}
		return c
	}
	cases := []map[string]interface{}{}
	dir, before := fixture("contention")
	lockBefore, _ := os.Stat(filepath.Join(dir, "catalog.lock"))
	winner := startWriterVMChild(t, dir, "hold")
	ready := winner.read(t)
	if ready["stage"] != "ready" || ready["expected"] != before.Digest() {
		t.Fatal(ready)
	}
	winner.send(t, "go")
	if winner.read(t)["stage"] != "held" {
		t.Fatal("not held")
	}
	_, e := NewReader(dir).Snapshot()
	assertCode(t, e, Busy)
	loser := startWriterVMChild(t, dir, "commit")
	other := loser.read(t)
	if other["expected"] != before.Digest() || other["pid"] == ready["pid"] {
		t.Fatal(other)
	}
	loser.send(t, "go")
	busy := loser.read(t)
	if busy["code"] != "Busy" {
		t.Fatal(busy)
	}
	loser.wait(t, 0)
	winner.send(t, "release")
	committed := winner.read(t)
	if committed["code"] != "Committed" {
		t.Fatal(committed)
	}
	winner.wait(t, 0)
	after := snapshot(dir)
	if after.Revision() != 2 || !after.Accounts()[0].Barrier || after.Accounts()[0].State != "recovering" {
		t.Fatal("not blocked")
	}
	preserved(before, after)
	metadata(dir)
	lockAfter, _ := os.Stat(filepath.Join(dir, "catalog.lock"))
	if !os.SameFile(lockBefore, lockAfter) {
		t.Fatal("lock replaced")
	}
	retry := startWriterVMChild(t, dir, "commit")
	retry.read(t)
	retry.send(t, "go")
	conflict := retry.read(t)
	if conflict["code"] != "Conflict" {
		t.Fatal(conflict)
	}
	retry.wait(t, 0)
	same, e := NewCatalogWriter(dir).BlockOrdinary(after, strings.Repeat("a", 32), strings.Repeat("e", 32))
	if e != nil || same.Digest() != after.Digest() {
		t.Fatal("negative ack", e)
	}
	cases = append(cases, map[string]interface{}{"case": "two-process-contention", "pids": []interface{}{ready["pid"], other["pid"]}, "same_expected_digest": before.Digest(), "winner": committed, "loser": busy, "retry": conflict, "reader_busy": true, "lock_inode_preserved": true, "peers_preserved": true, "root_metadata": true})
	for _, phase := range []string{"file-synced", "renamed", "directory-synced"} {
		dir, before := fixture(phase)
		child := startWriterVMChild(t, dir, phase)
		child.read(t)
		child.send(t, "go")
		child.wait(t, 19)
		after := snapshot(dir)
		preserved(before, after)
		metadata(dir)
		temporary, _ := filepath.Glob(filepath.Join(dir, ".catalog-*.tmp"))
		if phase == "file-synced" {
			if after.Digest() != before.Digest() || len(temporary) != 1 {
				t.Fatal("temp adopted", temporary)
			}
			_, e = NewCatalogWriter(dir).BlockOrdinary(before, strings.Repeat("a", 32), strings.Repeat("e", 32))
			if e != nil {
				t.Fatal(e)
			}
		} else {
			if !after.Accounts()[0].Barrier || after.Accounts()[0].State != "recovering" || len(temporary) != 0 {
				t.Fatal("cut reopened")
			}
			_, e = NewCatalogWriter(dir).BlockOrdinary(before, strings.Repeat("a", 32), strings.Repeat("e", 32))
			assertCode(t, e, Conflict)
			next, e := NewCatalogWriter(dir).BlockOrdinary(after, strings.Repeat("a", 32), strings.Repeat("e", 32))
			if e != nil || next.Digest() != after.Digest() {
				t.Fatal(e)
			}
		}
		cases = append(cases, map[string]interface{}{"case": phase, "exit_code": 19, "before_rename": phase == "file-synced", "orphan_temporary": len(temporary), "peers_preserved": true, "root_metadata": true, "no_positive_recovery": true})
	}
	data, e := json.Marshal(map[string]interface{}{"passed": true, "cases": cases, "default_root_writer": true, "real_process_contention": true, "account_mutations": false, "bootstrap": false, "positive_authority_recovery": false, "production_integrated": false})
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile("/root/lyra-go-writer-test/go-result.json", data, 0600); e != nil {
		t.Fatal(e)
	}
}
