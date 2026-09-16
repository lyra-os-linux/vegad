package nvidiarecovery

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
)

// Record is deliberately public: never include passwords, private paths,
// package inventories, machine IDs or repository contents here.
type Record struct {
	Kind      string `json:"kind"`
	Reference string `json:"reference"`
	State     string `json:"state"`
}

func (r Record) valid() bool {
	if r.Kind == "restic-offline" {
		if !referenceRE.MatchString(r.Reference) {
			return false
		}
	} else if r.Kind == "snapper" {
		n, err := strconv.ParseUint(r.Reference, 10, 32)
		if err != nil || n == 0 {
			return false
		}
	} else {
		return false
	}
	switch r.State {
	case "ready", "installed", "failed", "restoring", "restored":
		return true
	}
	return false
}

func WriteRecord(base string, r Record) error {
	if !r.valid() {
		return errors.New("invalid public recovery record")
	}
	if err := directory(base, 0755); err != nil {
		return err
	}
	return atomicJSON(filepath.Join(base, "recovery.json"), r, 0644)
}

func ReadRecord(base string) (Record, error) {
	var r Record
	if err := owned(base, true); err != nil {
		return r, err
	}
	path := filepath.Join(base, "recovery.json")
	if err := owned(path, false); err != nil {
		return r, err
	}
	b, err := readLimited(path, 4096)
	if err != nil {
		return r, err
	}
	if err = json.Unmarshal(b, &r); err != nil {
		return r, err
	}
	if !r.valid() {
		return Record{}, errors.New("invalid public recovery record")
	}
	return r, nil
}

func RecoveryRequired() bool {
	_, err := os.Lstat(filepath.Join(Base, "recovery-required"))
	return !errors.Is(err, os.ErrNotExist)
}
