// Package fsutil holds the small filesystem and identity helpers that
// App Runner's state-owning subsystems share.
package fsutil

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
)

// RandomID returns 32 hexadecimal characters of randomness, used for VM,
// device, and change identities.
func RandomID() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

// RemoveIfExists deletes a file, treating a missing file as success.
func RemoveIfExists(path string) error {
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// WriteFileAtomic replaces path's contents so that readers only ever see
// the previous or the complete new file, never a partial write.
func WriteFileAtomic(path string, contents []byte, mode os.FileMode) error {
	temporaryPath := path + ".tmp"
	if err := os.WriteFile(temporaryPath, contents, mode); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		_ = os.Remove(temporaryPath)
		return err
	}
	return nil
}
