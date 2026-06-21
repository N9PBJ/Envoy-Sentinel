package state

import (
	"path/filepath"
	"testing"

	"drlistener/internal/detector"
)

func TestSaveAtomicallyReplacesExistingSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "state.json")
	if err := Save(path, detector.Snapshot{State: detector.Active, StartSOC: 80}); err != nil {
		t.Fatal(err)
	}
	if err := Save(path, detector.Snapshot{State: detector.Inactive}); err != nil {
		t.Fatal(err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != detector.Inactive {
		t.Fatalf("state=%s want inactive", got.State)
	}
}
