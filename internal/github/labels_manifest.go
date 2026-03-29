package ghcli

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"strings"
	"sync"

	skillassets "github.com/nicobistolfi/vigilante"
	"github.com/nicobistolfi/vigilante/internal/backend"
)

type RepositoryLabelSpec = backend.RepositoryLabelSpec

type labelsManifest struct {
	Labels []RepositoryLabelSpec `json:"labels"`
}

var (
	manifestLabelsOnce sync.Once
	manifestLabels     []RepositoryLabelSpec
	manifestLabelsErr  error
)

func LoadRepositoryLabelSpecs() ([]RepositoryLabelSpec, error) {
	manifestLabelsOnce.Do(func() {
		data, err := fs.ReadFile(skillassets.LabelsManifest, ".github/labels.json")
		if err != nil {
			manifestLabelsErr = fmt.Errorf("read embedded labels manifest: %w", err)
			return
		}

		var manifest labelsManifest
		if err := json.Unmarshal(data, &manifest); err != nil {
			manifestLabelsErr = fmt.Errorf("parse embedded labels manifest: %w", err)
			return
		}

		manifestLabels = make([]RepositoryLabelSpec, 0, len(manifest.Labels))
		for _, label := range manifest.Labels {
			label.Name = strings.TrimSpace(label.Name)
			label.Color = strings.TrimSpace(label.Color)
			label.Description = strings.TrimSpace(label.Description)
			if label.Name == "" {
				continue
			}
			manifestLabels = append(manifestLabels, label)
		}
	})
	if manifestLabelsErr != nil {
		return nil, manifestLabelsErr
	}
	labels := make([]RepositoryLabelSpec, len(manifestLabels))
	copy(labels, manifestLabels)
	return labels, nil
}
