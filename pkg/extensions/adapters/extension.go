// Package extension provides the extension loading and management system.
// Extensions are self-contained modules that add channels, tools, providers,
// and other capabilities to AnyClaw, similar to OpenClaw's extensions/ architecture.
package extension

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
)

const manifestFileName = "anyclaw.extension.json"

var allowedManifestKinds = map[string]struct{}{
	"channel":  {},
	"tool":     {},
	"provider": {},
	"memory":   {},
	"hook":     {},
}

// Manifest defines the extension metadata (anyclaw.extension.json).
type Manifest struct {
	ID           string         `json:"id"`
	Name         string         `json:"name"`
	Version      string         `json:"version"`
	Description  string         `json:"description"`
	Kind         string         `json:"kind"` // "channel", "tool", "provider", "memory", "hook"
	Builtin      bool           `json:"builtin,omitempty"`
	Channels     []string       `json:"channels,omitempty"`      // Channel IDs this extension provides
	Providers    []string       `json:"providers,omitempty"`     // LLM provider IDs
	Skills       []string       `json:"skills,omitempty"`        // Skill IDs bundled
	Entrypoint   string         `json:"entrypoint"`              // Main executable/script
	Permissions  []string       `json:"permissions,omitempty"`   // Required permissions
	ConfigSchema map[string]any `json:"config_schema,omitempty"` // JSON Schema for config
}

// Extension represents a loaded extension with runtime state.
type Extension struct {
	Manifest Manifest
	Path     string
	Enabled  bool
	Config   map[string]any
}

// Registry manages all loaded extensions.
type Registry struct {
	mu            sync.RWMutex
	extensions    map[string]*Extension
	extensionsDir string
}

type discoveredManifest struct {
	Manifest Manifest
	Path     string
}

// NewRegistry creates a new extension registry.
func NewRegistry(extensionsDir string) *Registry {
	return &Registry{
		extensions:    make(map[string]*Extension),
		extensionsDir: extensionsDir,
	}
}

// Discover scans the extensions directory for available extensions.
func (r *Registry) Discover() ([]Manifest, error) {
	discovered, err := r.discover()
	if err != nil {
		return nil, err
	}

	manifests := make([]Manifest, 0, len(discovered))
	for _, item := range discovered {
		manifests = append(manifests, cloneManifest(item.Manifest))
	}
	return manifests, nil
}

func (r *Registry) discover() ([]discoveredManifest, error) {
	seen := make(map[string]struct{})
	manifests := make([]discoveredManifest, 0, len(builtinExtensionManifests()))

	for _, m := range builtinExtensionManifests() {
		if err := validateManifest(m); err != nil {
			return nil, fmt.Errorf("invalid builtin manifest %s: %w", m.ID, err)
		}
		if _, ok := seen[m.ID]; ok {
			return nil, fmt.Errorf("duplicate extension manifest %q", m.ID)
		}
		seen[m.ID] = struct{}{}
		manifests = append(manifests, discoveredManifest{
			Manifest: cloneManifest(m),
			Path:     "builtin://extensions/" + m.ID,
		})
	}

	entries, err := os.ReadDir(r.extensionsDir)
	if err != nil {
		if os.IsNotExist(err) {
			return manifests, nil
		}
		return nil, fmt.Errorf("failed to read extensions dir: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Name() < entries[j].Name()
	})

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		extPath := filepath.Join(r.extensionsDir, entry.Name())
		manifestPath := filepath.Join(extPath, manifestFileName)
		data, err := os.ReadFile(manifestPath)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return nil, fmt.Errorf("failed to read manifest %s: %w", manifestPath, err)
		}

		var m Manifest
		if err := json.Unmarshal(data, &m); err != nil {
			return nil, fmt.Errorf("invalid manifest %s: %w", manifestPath, err)
		}
		if err := validateManifest(m); err != nil {
			return nil, fmt.Errorf("invalid manifest %s: %w", manifestPath, err)
		}
		if _, ok := seen[m.ID]; ok {
			return nil, fmt.Errorf("duplicate extension manifest %q", m.ID)
		}

		seen[m.ID] = struct{}{}
		manifests = append(manifests, discoveredManifest{
			Manifest: cloneManifest(m),
			Path:     extPath,
		})
	}

	return manifests, nil
}

// Register adds an extension to the registry.
func (r *Registry) Register(ext *Extension) error {
	if ext == nil {
		return fmt.Errorf("extension is nil")
	}
	if err := validateManifest(ext.Manifest); err != nil {
		return err
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.extensions[ext.Manifest.ID]; ok {
		return fmt.Errorf("extension %q already registered", ext.Manifest.ID)
	}
	r.extensions[ext.Manifest.ID] = cloneExtension(ext)
	return nil
}

// Get returns an extension by ID.
func (r *Registry) Get(id string) (*Extension, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ext, ok := r.extensions[id]
	if !ok {
		return nil, false
	}
	return cloneExtension(ext), true
}

// List returns all registered extensions.
func (r *Registry) List() []*Extension {
	r.mu.RLock()
	defer r.mu.RUnlock()
	result := make([]*Extension, 0, len(r.extensions))
	for _, ext := range r.extensions {
		result = append(result, cloneExtension(ext))
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Manifest.ID < result[j].Manifest.ID
	})
	return result
}

// ListByKind returns extensions filtered by kind.
func (r *Registry) ListByKind(kind string) []*Extension {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var result []*Extension
	for _, ext := range r.extensions {
		if ext.Manifest.Kind == kind && ext.Enabled {
			result = append(result, cloneExtension(ext))
		}
	}
	sort.Slice(result, func(i, j int) bool {
		return result[i].Manifest.ID < result[j].Manifest.ID
	})
	return result
}

// Enable marks an extension as enabled.
func (r *Registry) Enable(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	ext, ok := r.extensions[id]
	if !ok {
		return fmt.Errorf("extension %q not found", id)
	}
	ext.Enabled = true
	return nil
}

// Disable marks an extension as disabled.
func (r *Registry) Disable(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	ext, ok := r.extensions[id]
	if !ok {
		return fmt.Errorf("extension %q not found", id)
	}
	ext.Enabled = false
	return nil
}

// LoadExtension loads a single extension from its directory.
func LoadExtension(dir string) (*Extension, error) {
	manifestPath := filepath.Join(dir, manifestFileName)
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read manifest: %w", err)
	}

	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("invalid manifest: %w", err)
	}
	if err := validateManifest(m); err != nil {
		return nil, fmt.Errorf("invalid manifest: %w", err)
	}

	return &Extension{
		Manifest: cloneManifest(m),
		Path:     dir,
		Enabled:  true,
	}, nil
}

// LoadAll discovers and loads all extensions from the registry's directory.
func (r *Registry) LoadAll() error {
	manifests, err := r.discover()
	if err != nil {
		return err
	}

	for _, item := range manifests {
		if item.Manifest.Builtin {
			if err := r.Register(&Extension{
				Manifest: item.Manifest,
				Path:     item.Path,
				Enabled:  true,
			}); err != nil {
				return err
			}
			continue
		}
		ext, err := LoadExtension(item.Path)
		if err != nil {
			return fmt.Errorf("failed to load extension %s: %w", item.Manifest.ID, err)
		}
		if err := r.Register(ext); err != nil {
			return err
		}
	}

	return nil
}

func validateManifest(m Manifest) error {
	if strings.TrimSpace(m.ID) == "" {
		return fmt.Errorf("manifest id is required")
	}
	if !isSafeID(m.ID) {
		return fmt.Errorf("manifest id %q is not safe", m.ID)
	}
	if strings.TrimSpace(m.Name) == "" {
		return fmt.Errorf("manifest name is required")
	}
	if strings.TrimSpace(m.Version) == "" {
		return fmt.Errorf("manifest version is required")
	}
	if _, ok := allowedManifestKinds[m.Kind]; !ok {
		return fmt.Errorf("manifest kind %q is not supported", m.Kind)
	}
	if m.Entrypoint != "" && !isSafeRelativePath(m.Entrypoint) {
		return fmt.Errorf("manifest entrypoint %q must be a relative path inside the extension", m.Entrypoint)
	}
	return nil
}

func isSafeID(id string) bool {
	if id == "." || id == ".." || filepath.IsAbs(id) {
		return false
	}
	return !strings.ContainsAny(id, `/\`)
}

func isSafeRelativePath(path string) bool {
	clean := filepath.Clean(path)
	if clean == "." || filepath.IsAbs(clean) || filepath.VolumeName(clean) != "" {
		return false
	}
	parts := strings.FieldsFunc(clean, func(r rune) bool {
		return r == '/' || r == '\\'
	})
	for _, part := range parts {
		if part == ".." {
			return false
		}
	}
	return true
}

func cloneExtension(ext *Extension) *Extension {
	if ext == nil {
		return nil
	}
	return &Extension{
		Manifest: cloneManifest(ext.Manifest),
		Path:     ext.Path,
		Enabled:  ext.Enabled,
		Config:   cloneMap(ext.Config),
	}
}

func cloneManifest(m Manifest) Manifest {
	m.Channels = append([]string(nil), m.Channels...)
	m.Providers = append([]string(nil), m.Providers...)
	m.Skills = append([]string(nil), m.Skills...)
	m.Permissions = append([]string(nil), m.Permissions...)
	m.ConfigSchema = cloneMap(m.ConfigSchema)
	return m
}

func cloneMap(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	output := make(map[string]any, len(input))
	for k, v := range input {
		output[k] = cloneValue(v)
	}
	return output
}

func cloneValue(value any) any {
	if value == nil {
		return nil
	}
	return cloneReflect(reflect.ValueOf(value)).Interface()
}

func cloneReflect(value reflect.Value) reflect.Value {
	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		cloned := cloneReflect(value.Elem())
		wrapped := reflect.New(value.Type()).Elem()
		wrapped.Set(cloned)
		return wrapped
	case reflect.Map:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		cloned := reflect.MakeMapWithSize(value.Type(), value.Len())
		iter := value.MapRange()
		for iter.Next() {
			cloned.SetMapIndex(cloneReflect(iter.Key()), cloneReflect(iter.Value()))
		}
		return cloned
	case reflect.Slice:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		cloned := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		for i := 0; i < value.Len(); i++ {
			cloned.Index(i).Set(cloneReflect(value.Index(i)))
		}
		return cloned
	case reflect.Array:
		cloned := reflect.New(value.Type()).Elem()
		for i := 0; i < value.Len(); i++ {
			cloned.Index(i).Set(cloneReflect(value.Index(i)))
		}
		return cloned
	default:
		return value
	}
}
