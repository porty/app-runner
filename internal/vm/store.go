package vm

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/roflware/app-runner/internal/fsutil"
)

type jsonStore struct {
	path string
}

func NewJSONStore(diskDirectory string) *jsonStore {
	return &jsonStore{path: filepath.Join(diskDirectory, "vms.json")}
}

func (s *jsonStore) Load() ([]VirtualMachine, error) {
	contents, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return []VirtualMachine{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read VM metadata: %w", err)
	}
	var vms []VirtualMachine
	if err := json.Unmarshal(contents, &vms); err != nil {
		return nil, fmt.Errorf("decode VM metadata: %w", err)
	}
	return vms, nil
}

func (s *jsonStore) Save(vms []VirtualMachine) error {
	contents, err := json.MarshalIndent(vms, "", "  ")
	if err != nil {
		return fmt.Errorf("encode VM metadata: %w", err)
	}
	contents = append(contents, '\n')
	if err := fsutil.WriteFileAtomic(s.path, contents, 0o600); err != nil {
		return fmt.Errorf("write VM metadata: %w", err)
	}
	return nil
}
