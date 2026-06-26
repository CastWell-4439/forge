package storage

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// AppendJSONL appends one JSON-encoded value as a single line.
func AppendJSONL[T any](path string, value T) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer file.Close()

	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if _, err := file.Write(encoded); err != nil {
		return err
	}
	_, err = file.Write([]byte("\n"))
	return err
}
