package apprunner

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

var (
	errCloudInitTemplateNotFound = errors.New("cloud-init template was not found")
	errCloudInitTemplateExists   = errors.New("a cloud-init template with that name already exists")
)

type cloudInitTemplate struct {
	Name      string    `json:"name"`
	UserData  string    `json:"user_data"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type cloudInitTemplateStore interface {
	Load() ([]cloudInitTemplate, error)
	Save([]cloudInitTemplate) error
}

type jsonCloudInitTemplateStore struct {
	path string
}

func newJSONCloudInitTemplateStore(diskDirectory string) *jsonCloudInitTemplateStore {
	return &jsonCloudInitTemplateStore{path: filepath.Join(diskDirectory, "cloud-init-templates.json")}
}

func (s *jsonCloudInitTemplateStore) Load() ([]cloudInitTemplate, error) {
	contents, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return []cloudInitTemplate{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read cloud-init templates: %w", err)
	}
	var templates []cloudInitTemplate
	if err := json.Unmarshal(contents, &templates); err != nil {
		return nil, fmt.Errorf("decode cloud-init templates: %w", err)
	}
	return templates, nil
}

func (s *jsonCloudInitTemplateStore) Save(templates []cloudInitTemplate) error {
	contents, err := json.MarshalIndent(templates, "", "  ")
	if err != nil {
		return fmt.Errorf("encode cloud-init templates: %w", err)
	}
	contents = append(contents, '\n')
	temporaryPath := s.path + ".tmp"
	if err := os.WriteFile(temporaryPath, contents, 0o600); err != nil {
		return fmt.Errorf("write cloud-init templates: %w", err)
	}
	if err := os.Rename(temporaryPath, s.path); err != nil {
		_ = os.Remove(temporaryPath)
		return fmt.Errorf("replace cloud-init templates: %w", err)
	}
	return nil
}

type cloudInitTemplateManager struct {
	mu        sync.Mutex
	store     cloudInitTemplateStore
	now       func() time.Time
	templates []cloudInitTemplate
}

func newCloudInitTemplateManager(store cloudInitTemplateStore) (*cloudInitTemplateManager, error) {
	templates, err := store.Load()
	if err != nil {
		return nil, err
	}
	manager := &cloudInitTemplateManager{store: store, now: time.Now, templates: templates}
	if err := manager.validateStoredTemplates(); err != nil {
		return nil, err
	}
	return manager, nil
}

func (m *cloudInitTemplateManager) List() []cloudInitTemplate {
	m.mu.Lock()
	defer m.mu.Unlock()
	templates := slices.Clone(m.templates)
	sort.Slice(templates, func(i, j int) bool {
		return strings.ToLower(templates[i].Name) < strings.ToLower(templates[j].Name)
	})
	return templates
}

func (m *cloudInitTemplateManager) Create(name, userData string) (cloudInitTemplate, error) {
	name = strings.TrimSpace(name)
	if err := validateCloudInitTemplate(name, userData); err != nil {
		return cloudInitTemplate{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.indexByName(name) != -1 {
		return cloudInitTemplate{}, errCloudInitTemplateExists
	}
	now := m.now().UTC()
	template := cloudInitTemplate{Name: name, UserData: userData, CreatedAt: now, UpdatedAt: now}
	m.templates = append(m.templates, template)
	if err := m.store.Save(m.templates); err != nil {
		m.templates = m.templates[:len(m.templates)-1]
		return cloudInitTemplate{}, err
	}
	return template, nil
}

func (m *cloudInitTemplateManager) Update(name, newName, userData string) (cloudInitTemplate, error) {
	name = strings.TrimSpace(name)
	newName = strings.TrimSpace(newName)
	if name == "" {
		return cloudInitTemplate{}, &fieldError{field: "name", message: "a template name is required"}
	}
	if err := validateCloudInitTemplate(newName, userData); err != nil {
		return cloudInitTemplate{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	index := m.indexByName(name)
	if index == -1 {
		return cloudInitTemplate{}, errCloudInitTemplateNotFound
	}
	if duplicate := m.indexByName(newName); duplicate != -1 && duplicate != index {
		return cloudInitTemplate{}, errCloudInitTemplateExists
	}
	previous := m.templates[index]
	m.templates[index].Name = newName
	m.templates[index].UserData = userData
	m.templates[index].UpdatedAt = m.now().UTC()
	if err := m.store.Save(m.templates); err != nil {
		m.templates[index] = previous
		return cloudInitTemplate{}, err
	}
	return m.templates[index], nil
}

func (m *cloudInitTemplateManager) Delete(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return &fieldError{field: "name", message: "a template name is required"}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	index := m.indexByName(name)
	if index == -1 {
		return errCloudInitTemplateNotFound
	}
	previous := slices.Clone(m.templates)
	m.templates = append(m.templates[:index], m.templates[index+1:]...)
	if err := m.store.Save(m.templates); err != nil {
		m.templates = previous
		return err
	}
	return nil
}

func (m *cloudInitTemplateManager) indexByName(name string) int {
	for index, template := range m.templates {
		if strings.EqualFold(template.Name, name) {
			return index
		}
	}
	return -1
}

func (m *cloudInitTemplateManager) validateStoredTemplates() error {
	seen := make(map[string]struct{}, len(m.templates))
	for _, template := range m.templates {
		if err := validateCloudInitTemplate(template.Name, template.UserData); err != nil {
			return fmt.Errorf("invalid stored cloud-init template %q: %w", template.Name, err)
		}
		key := strings.ToLower(template.Name)
		if _, exists := seen[key]; exists {
			return fmt.Errorf("duplicate stored cloud-init template %q", template.Name)
		}
		seen[key] = struct{}{}
	}
	return nil
}

func validateCloudInitTemplate(name, userData string) error {
	if name == "" {
		return &fieldError{field: "name", message: "a template name is required"}
	}
	if len(name) > 80 {
		return &fieldError{field: "name", message: "must not exceed 80 characters"}
	}
	if strings.ContainsAny(name, "\r\n\t") {
		return &fieldError{field: "name", message: "must not contain control characters"}
	}
	if len(userData) > maxCloudInitUserDataBytes {
		return &fieldError{field: "user_data", message: "must not exceed 1 MiB"}
	}
	return nil
}
