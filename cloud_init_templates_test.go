package apprunner

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

type memoryCloudInitTemplateStore struct {
	templates []cloudInitTemplate
	err       error
}

func (s *memoryCloudInitTemplateStore) Load() ([]cloudInitTemplate, error) {
	return slices.Clone(s.templates), s.err
}

func (s *memoryCloudInitTemplateStore) Save(templates []cloudInitTemplate) error {
	if s.err != nil {
		return s.err
	}
	s.templates = slices.Clone(templates)
	return nil
}

func TestCloudInitTemplateManagerLifecycle(t *testing.T) {
	store := &memoryCloudInitTemplateStore{}
	manager, err := newCloudInitTemplateManager(store)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.August, 24, 4, 0, 0, 0, time.UTC)
	manager.now = func() time.Time { return now }

	template, err := manager.Create("Ubuntu base", "#cloud-config\npackages: [curl]\n")
	if err != nil {
		t.Fatal(err)
	}
	if template.Name != "Ubuntu base" || !template.CreatedAt.Equal(now) || !template.UpdatedAt.Equal(now) {
		t.Fatalf("unexpected created template: %#v", template)
	}
	if _, err := manager.Create("ubuntu BASE", "different"); !errors.Is(err, errCloudInitTemplateExists) {
		t.Fatalf("case-insensitive duplicate returned %v", err)
	}

	now = now.Add(time.Hour)
	template, err = manager.Update("ubuntu base", "Ubuntu server", "#!/bin/sh\necho ready\n")
	if err != nil {
		t.Fatal(err)
	}
	if template.Name != "Ubuntu server" || template.UserData != "#!/bin/sh\necho ready\n" || !template.UpdatedAt.Equal(now) {
		t.Fatalf("unexpected updated template: %#v", template)
	}
	if templates := manager.List(); len(templates) != 1 || templates[0] != template {
		t.Fatalf("unexpected template listing: %#v", templates)
	}
	if err := manager.Delete("UBUNTU SERVER"); err != nil {
		t.Fatal(err)
	}
	if len(manager.List()) != 0 {
		t.Fatal("deleted template remained in the listing")
	}
	if err := manager.Delete("Ubuntu server"); !errors.Is(err, errCloudInitTemplateNotFound) {
		t.Fatalf("deleting a missing template returned %v", err)
	}
}

func TestCloudInitTemplateManagerRollsBackStoreFailures(t *testing.T) {
	store := &memoryCloudInitTemplateStore{}
	manager, err := newCloudInitTemplateManager(store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Create("base", "original"); err != nil {
		t.Fatal(err)
	}
	store.err = errors.New("disk full")
	if _, err := manager.Update("base", "renamed", "changed"); err == nil {
		t.Fatal("update unexpectedly succeeded")
	}
	if templates := manager.List(); len(templates) != 1 || templates[0].Name != "base" || templates[0].UserData != "original" {
		t.Fatalf("failed update was not rolled back: %#v", templates)
	}
	if err := manager.Delete("base"); err == nil {
		t.Fatal("delete unexpectedly succeeded")
	}
	if templates := manager.List(); len(templates) != 1 || templates[0].Name != "base" {
		t.Fatalf("failed delete was not rolled back: %#v", templates)
	}
}

func TestJSONCloudInitTemplateStorePersistsPrivateFile(t *testing.T) {
	directory := t.TempDir()
	store := newJSONCloudInitTemplateStore(directory)
	templates := []cloudInitTemplate{{
		Name: "base", UserData: "#cloud-config\n", CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}}
	if err := store.Save(templates); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load()
	if err != nil || len(loaded) != 1 || loaded[0].Name != "base" || loaded[0].UserData != "#cloud-config\n" {
		t.Fatalf("loaded templates = %#v, %v", loaded, err)
	}
	info, err := os.Stat(filepath.Join(directory, "cloud-init-templates.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("template file mode = %o, want 600", info.Mode().Perm())
	}
}
