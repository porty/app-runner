package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type jsonVMStore struct {
	path string
}

func newJSONVMStore(diskDirectory string) *jsonVMStore {
	return &jsonVMStore{path: filepath.Join(diskDirectory, "vms.json")}
}

func (s *jsonVMStore) Load() ([]virtualMachine, error) {
	contents, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return []virtualMachine{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read VM metadata: %w", err)
	}
	var vms []virtualMachine
	if err := json.Unmarshal(contents, &vms); err != nil {
		return nil, fmt.Errorf("decode VM metadata: %w", err)
	}
	return vms, nil
}

func (s *jsonVMStore) Save(vms []virtualMachine) error {
	contents, err := json.MarshalIndent(vms, "", "  ")
	if err != nil {
		return fmt.Errorf("encode VM metadata: %w", err)
	}
	contents = append(contents, '\n')
	temporaryPath := s.path + ".tmp"
	if err := os.WriteFile(temporaryPath, contents, 0o600); err != nil {
		return fmt.Errorf("write VM metadata: %w", err)
	}
	if err := os.Rename(temporaryPath, s.path); err != nil {
		_ = os.Remove(temporaryPath)
		return fmt.Errorf("replace VM metadata: %w", err)
	}
	return nil
}
