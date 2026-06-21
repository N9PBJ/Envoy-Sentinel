package state

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"drlistener/internal/detector"
)

func Load(path string) (detector.Snapshot, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return detector.Snapshot{}, nil
		}
		return detector.Snapshot{}, err
	}
	var snapshot detector.Snapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return detector.Snapshot{}, err
	}
	return snapshot, nil
}

func Save(path string, snapshot detector.Snapshot) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}

	// Write and flush a sibling temporary file before replacing the state file.
	// A crash can leave the old or new snapshot, but not a half-written one.
	temp, err := os.CreateTemp(dir, ".drlistener-state-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer func() { _ = os.Remove(tempPath) }()

	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return err
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}
