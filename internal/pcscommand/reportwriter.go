package pcscommand

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// writeJSONReport writes a timestamped JSON report into dir.
func writeJSONReport(dir, prefix string, payload interface{}) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := prefix + "-" + time.Now().Format("20060102-150405") + ".json"
	full := filepath.Join(dir, name)
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(full, data, 0o644); err != nil {
		return "", err
	}
	return full, nil
}
