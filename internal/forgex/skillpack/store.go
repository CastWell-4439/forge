package skillpack

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// Store reads and writes SkillPack documents under one skills root:
// published packs live in the root, drafts in a drafts/ subdirectory, so a
// pack is visible only after it passed its gates.
type Store struct {
	Root string
}

// NewStore creates a store rooted at a skills directory.
func NewStore(root string) Store {
	if strings.TrimSpace(root) == "" {
		root = "configs/forgex/skills"
	}
	return Store{Root: root}
}

// DraftsDir is where distillation writes its drafts.
func (s Store) DraftsDir() string { return filepath.Join(s.Root, "drafts") }

// Save writes a pack. draft selects the drafts directory or the published one
// and stamps the matching status, so the status field can never disagree with
// where the file lives.
func (s Store) Save(p Pack, draft bool) (string, error) {
	if p.Metadata.ID == "" {
		return "", fmt.Errorf("save: pack id is required")
	}
	dir := s.Root
	if draft {
		dir = s.DraftsDir()
		p.Metadata.Status = StatusDraft
	} else {
		p.Metadata.Status = StatusPublished
	}
	if p.Kind == "" {
		p.Kind = Kind
	}
	if p.APIVersion == "" {
		p.APIVersion = APIVersion
	}

	data, err := yaml.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("save %s: %w", p.Metadata.ID, err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("save %s: %w", p.Metadata.ID, err)
	}
	path := filepath.Join(dir, p.Metadata.ID+".yaml")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", fmt.Errorf("save %s: %w", p.Metadata.ID, err)
	}
	return path, nil
}

// Load reads a published pack by id.
func (s Store) Load(id string) (Pack, error) {
	return readPack(filepath.Join(s.Root, id+".yaml"))
}

// LoadPath reads a pack from an explicit path; used for drafts under review.
func LoadPath(path string) (Pack, error) {
	return readPack(path)
}

// List returns every published pack, sorted by id.
func (s Store) List() ([]Pack, error) {
	entries, err := os.ReadDir(s.Root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("list: %w", err)
	}
	var out []Pack
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") {
			continue
		}
		pack, err := readPack(filepath.Join(s.Root, entry.Name()))
		if err != nil {
			return nil, err
		}
		out = append(out, pack)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Metadata.ID < out[j].Metadata.ID })
	return out, nil
}

func readPack(path string) (Pack, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Pack{}, fmt.Errorf("read pack %s: %w", path, err)
	}
	var p Pack
	if err := yaml.Unmarshal(data, &p); err != nil {
		return Pack{}, fmt.Errorf("parse pack %s: %w", path, err)
	}
	return p, nil
}
