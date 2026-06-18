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
	if err := os.MkdirAll(filepath.Dir(pathOrDot(path)), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func pathOrDot(path string) string {
	if filepath.Dir(path) == "." {
		return "."
	}
	return path
}
