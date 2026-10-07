package parental

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"unicode/utf8"
)

const InvalidFlatpakApprovalPolicy Code = "InvalidFlatpakApprovalPolicy"
const flatpakApprovalLimit = 65536
const maxFlatpakApprovals = 128

// FlatpakApprovalPolicy is immutable server state for one supervised account.
// Its digest must match the trusted Flatpak adapter digest observed under the
// account lease. Decoding alone does not authenticate a writer or authorize a
// launch; no production writer or storage source exists yet.
type FlatpakApprovalPolicy struct {
	association, generation, digest string
	uid                             uint32
	approvals                       map[string]FlatpakApproval
}

func (p FlatpakApprovalPolicy) Digest() string { return p.digest }

// Approved returns only an explicitly listed app. An empty policy denies all
// user apps while the graphical session's essential infrastructure is handled
// by its separate contract.
func (p FlatpakApprovalPolicy) Approved(request FlatpakLaunchRequest,
	binding FlatpakPeerBinding) (FlatpakApproval, error) {
	if p.association != binding.Association || p.generation != binding.Generation ||
		p.uid != binding.UID || !launchAppID.MatchString(request.AppID) {
		return FlatpakApproval{}, Blocked
	}
	approval, ok := p.approvals[request.AppID]
	if !ok {
		return FlatpakApproval{}, Blocked
	}
	return approval, nil
}

// DecodeFlatpakApprovalPolicy rejects duplicate/extra fields and demands an
// exact digest previously published by the Flatpak producer for this account.
// This prevents a root-owned policy file from silently changing independently
// of the account's observed adapter generation; it is not anti-rollback proof.
func DecodeFlatpakApprovalPolicy(data []byte, binding FlatpakPeerBinding,
	expectedAdapterDigest string) (FlatpakApprovalPolicy, error) {
	if len(data) == 0 || len(data) > flatpakApprovalLimit || !utf8.Valid(data) ||
		!hash.MatchString(expectedAdapterDigest) ||
		!token.MatchString(binding.Association) || !token.MatchString(binding.Generation) || binding.UID == 0 {
		return FlatpakApprovalPolicy{}, InvalidFlatpakApprovalPolicy
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	raw, err := readJSON(d, 0)
	if err != nil {
		return FlatpakApprovalPolicy{}, InvalidFlatpakApprovalPolicy
	}
	if _, err = d.Token(); err != io.EOF {
		return FlatpakApprovalPolicy{}, InvalidFlatpakApprovalPolicy
	}
	row, ok := object(raw, "schema", "association", "generation", "uid", "approvals")
	if !ok {
		return FlatpakApprovalPolicy{}, InvalidFlatpakApprovalPolicy
	}
	schema, ok := positive(row["schema"], 1)
	if !ok || schema != 1 || row["association"] != binding.Association ||
		row["generation"] != binding.Generation {
		return FlatpakApprovalPolicy{}, InvalidFlatpakApprovalPolicy
	}
	uid, ok := positive(row["uid"], 4294967294)
	if !ok || uint32(uid) != binding.UID {
		return FlatpakApprovalPolicy{}, InvalidFlatpakApprovalPolicy
	}
	entries, ok := row["approvals"].([]any)
	if !ok || len(entries) > maxFlatpakApprovals {
		return FlatpakApprovalPolicy{}, InvalidFlatpakApprovalPolicy
	}
	p := FlatpakApprovalPolicy{association: binding.Association, generation: binding.Generation,
		uid: binding.UID, approvals: make(map[string]FlatpakApproval, len(entries))}
	for _, entry := range entries {
		a, valid := object(entry, "ref", "scope", "installation", "origin", "publisher_fingerprint", "commit")
		if !valid {
			return FlatpakApprovalPolicy{}, InvalidFlatpakApprovalPolicy
		}
		ref, ok := a["ref"].(string)
		if !ok {
			return FlatpakApprovalPolicy{}, InvalidFlatpakApprovalPolicy
		}
		id, ok := flatpakFullRef(ref, "app")
		if !ok || p.approvals[id].Ref != "" {
			return FlatpakApprovalPolicy{}, InvalidFlatpakApprovalPolicy
		}
		approval := FlatpakApproval{Ref: ref}
		if approval.Scope, ok = a["scope"].(string); !ok {
			return FlatpakApprovalPolicy{}, InvalidFlatpakApprovalPolicy
		}
		if approval.Installation, ok = a["installation"].(string); !ok {
			return FlatpakApprovalPolicy{}, InvalidFlatpakApprovalPolicy
		}
		if approval.Scope == "user" {
			approval.OwnerUID = binding.UID
		}
		if !validFlatpakScope(approval.Scope, approval.Installation, approval.OwnerUID, binding.UID) {
			return FlatpakApprovalPolicy{}, InvalidFlatpakApprovalPolicy
		}
		if approval.Origin, ok = a["origin"].(string); !ok || !flatpakRefComponent.MatchString(approval.Origin) {
			return FlatpakApprovalPolicy{}, InvalidFlatpakApprovalPolicy
		}
		if approval.PublisherFingerprint, ok = a["publisher_fingerprint"].(string); !ok ||
			approval.PublisherFingerprint != "" && !flatpakPublisher.MatchString(approval.PublisherFingerprint) {
			return FlatpakApprovalPolicy{}, InvalidFlatpakApprovalPolicy
		}
		if approval.Commit, ok = a["commit"].(string); !ok ||
			approval.Commit != "" && !hash.MatchString(approval.Commit) ||
			approval.PublisherFingerprint == "" && !hash.MatchString(approval.Commit) {
			return FlatpakApprovalPolicy{}, InvalidFlatpakApprovalPolicy
		}
		p.approvals[id] = approval
	}
	canonical, err := json.Marshal(raw)
	if err != nil {
		return FlatpakApprovalPolicy{}, InvalidFlatpakApprovalPolicy
	}
	sum := sha256.Sum256(canonical)
	p.digest = hex.EncodeToString(sum[:])
	if p.digest != expectedAdapterDigest {
		return FlatpakApprovalPolicy{}, InvalidFlatpakApprovalPolicy
	}
	return p, nil
}
