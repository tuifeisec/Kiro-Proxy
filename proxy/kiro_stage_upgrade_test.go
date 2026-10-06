package proxy

import (
	"bytes"
	"testing"
)

func TestKiroStageUpgradeReconstructsOnlyExactKnownBridge(t *testing.T) {
	original := []byte("first anchor; second anchor")
	previous := []byte("old first; second anchor")
	candidate := []byte("new first; new second")
	manifest := kiroStageManifest{
		OriginalSHA256: kiroStageSHA256(original), CandidateSHA256: kiroStageSHA256(candidate),
		Patches: []kiroStagePatch{
			{Name: "first", Before: "first anchor", After: "new first"},
			{Name: "second", Before: "second anchor", After: "new second"},
		},
		PreviousVersions: []kiroStagePreviousVersion{{
			CandidateSHA256: kiroStageSHA256(previous),
			Patches:         []kiroStagePatch{{Name: "old-first", Before: "first anchor", After: "old first"}},
		}},
	}
	for _, source := range [][]byte{original, previous, candidate} {
		before := append([]byte(nil), source...)
		got, err := buildKiroStageUpgrade(source, manifest)
		if err != nil || !bytes.Equal(got, candidate) || !bytes.Equal(source, before) {
			t.Fatalf("upgrade failed or mutated source: %s, %v", got, err)
		}
	}
	if _, err := buildKiroStageUpgrade([]byte("unknown bundle"), manifest); err == nil {
		t.Fatal("unknown bundle was accepted")
	}
	// A known hash is insufficient when its inverse patches reconstruct a
	// different original. Every upgrade must verify the original full hash.
	manifest.PreviousVersions[0].Patches[0].Before = "wrong original"
	if _, err := buildKiroStageUpgrade(previous, manifest); err == nil {
		t.Fatal("incorrect inverse patch bypassed original hash verification")
	}
}

func TestKiroStageUpgradeRejectsNonUniqueReverseAnchor(t *testing.T) {
	previous := []byte("old old")
	manifest := kiroStageManifest{
		OriginalSHA256: kiroStageSHA256([]byte("original")), CandidateSHA256: kiroStageSHA256([]byte("new")),
		PreviousVersions: []kiroStagePreviousVersion{{
			CandidateSHA256: kiroStageSHA256(previous),
			Patches:         []kiroStagePatch{{Name: "duplicate", Before: "original", After: "old"}},
		}},
	}
	if _, err := buildKiroStageUpgrade(previous, manifest); err == nil {
		t.Fatal("non-unique reverse anchor was accepted")
	}
}
