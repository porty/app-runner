package cloudinit

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

	"github.com/roflware/app-runner/internal/fsutil"
	"github.com/roflware/app-runner/internal/validate"
)

var (
	ErrTemplateNotFound = errors.New("cloud-init template was not found")
	ErrTemplateExists   = errors.New("a cloud-init template with that name already exists")
)

type Template struct {
	Name      string    `json:"name"`
	UserData  string    `json:"user_data"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type TemplateStore interface {
	Load() ([]Template, error)
	Save([]Template) error
}

type jsonTemplateStore struct {
	path string
}

func NewJSONTemplateStore(diskDirectory string) *jsonTemplateStore {
	return &jsonTemplateStore{path: filepath.Join(diskDirectory, "cloud-init-templates.json")}
}

func (s *jsonTemplateStore) Load() ([]Template, error) {
	contents, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return []Template{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read cloud-init templates: %w", err)
	}
	var templates []Template
	if err := json.Unmarshal(contents, &templates); err != nil {
		return nil, fmt.Errorf("decode cloud-init templates: %w", err)
	}
	return templates, nil
}

func (s *jsonTemplateStore) Save(templates []Template) error {
	contents, err := json.MarshalIndent(templates, "", "  ")
	if err != nil {
		return fmt.Errorf("encode cloud-init templates: %w", err)
	}
	contents = append(contents, '\n')
	if err := fsutil.WriteFileAtomic(s.path, contents, 0o600); err != nil {
		return fmt.Errorf("write cloud-init templates: %w", err)
	}
	return nil
}

type TemplateManager struct {
	mu        sync.Mutex
	store     TemplateStore
	now       func() time.Time
	templates []Template
}

func NewTemplateManager(store TemplateStore) (*TemplateManager, error) {
	templates, err := store.Load()
	if err != nil {
		return nil, err
	}
	manager := &TemplateManager{store: store, now: time.Now, templates: templates}
	if err := manager.validateStoredTemplates(); err != nil {
		return nil, err
	}
	return manager, nil
}

func (m *TemplateManager) List() []Template {
	m.mu.Lock()
	defer m.mu.Unlock()
	templates := slices.Clone(m.templates)
	sort.Slice(templates, func(i, j int) bool {
		return strings.ToLower(templates[i].Name) < strings.ToLower(templates[j].Name)
	})
	return templates
}

func (m *TemplateManager) Create(name, userData string) (Template, error) {
	name = strings.TrimSpace(name)
	if err := validateTemplate(name, userData); err != nil {
		return Template{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.indexByName(name) != -1 {
		return Template{}, ErrTemplateExists
	}
	now := m.now().UTC()
	template := Template{Name: name, UserData: userData, CreatedAt: now, UpdatedAt: now}
	m.templates = append(m.templates, template)
	if err := m.store.Save(m.templates); err != nil {
		m.templates = m.templates[:len(m.templates)-1]
		return Template{}, err
	}
	return template, nil
}

func (m *TemplateManager) Update(name, newName, userData string) (Template, error) {
	name = strings.TrimSpace(name)
	newName = strings.TrimSpace(newName)
	if name == "" {
		return Template{}, validate.Field("name", "a template name is required")
	}
	if err := validateTemplate(newName, userData); err != nil {
		return Template{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	index := m.indexByName(name)
	if index == -1 {
		return Template{}, ErrTemplateNotFound
	}
	if duplicate := m.indexByName(newName); duplicate != -1 && duplicate != index {
		return Template{}, ErrTemplateExists
	}
	previous := m.templates[index]
	m.templates[index].Name = newName
	m.templates[index].UserData = userData
	m.templates[index].UpdatedAt = m.now().UTC()
	if err := m.store.Save(m.templates); err != nil {
		m.templates[index] = previous
		return Template{}, err
	}
	return m.templates[index], nil
}

func (m *TemplateManager) Delete(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return validate.Field("name", "a template name is required")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	index := m.indexByName(name)
	if index == -1 {
		return ErrTemplateNotFound
	}
	previous := slices.Clone(m.templates)
	m.templates = append(m.templates[:index], m.templates[index+1:]...)
	if err := m.store.Save(m.templates); err != nil {
		m.templates = previous
		return err
	}
	return nil
}

func (m *TemplateManager) indexByName(name string) int {
	for index, template := range m.templates {
		if strings.EqualFold(template.Name, name) {
			return index
		}
	}
	return -1
}

func (m *TemplateManager) validateStoredTemplates() error {
	seen := make(map[string]struct{}, len(m.templates))
	for _, template := range m.templates {
		if err := validateTemplate(template.Name, template.UserData); err != nil {
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

func validateTemplate(name, userData string) error {
	if name == "" {
		return validate.Field("name", "a template name is required")
	}
	if len(name) > 80 {
		return validate.Field("name", "must not exceed 80 characters")
	}
	if strings.ContainsAny(name, "\r\n\t") {
		return validate.Field("name", "must not contain control characters")
	}
	if len(userData) > MaxUserDataBytes {
		return validate.Field("user_data", "must not exceed 1 MiB")
	}
	return nil
}
