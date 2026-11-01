package manifest

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

const CurrentSchemaVersion = "1"

var (
	gitSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)
	digest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
)

type Image struct {
	Name   string `json:"name"`
	Digest string `json:"digest"`
}

type Release struct {
	SchemaVersion       string    `json:"schema_version"`
	ReleaseID           string    `json:"release_id"`
	GitSHA              string    `json:"git_sha"`
	Images              []Image   `json:"images"`
	MigrationVersion    string    `json:"migration_version"`
	ConfigSchemaVersion string    `json:"config_schema_version"`
	BuiltAt             time.Time `json:"built_at"`
	PreviousLKGRelease  string    `json:"previous_lkg_release_id"`
}

func (r Release) Validate() error {
	if r.SchemaVersion != CurrentSchemaVersion {
		return fmt.Errorf("schema_version must be %q", CurrentSchemaVersion)
	}
	if strings.TrimSpace(r.ReleaseID) == "" {
		return errors.New("release_id is required")
	}
	if !gitSHA.MatchString(r.GitSHA) {
		return errors.New("git_sha must be a lowercase 40-character SHA")
	}
	if len(r.Images) == 0 {
		return errors.New("at least one image is required")
	}
	seen := make(map[string]struct{}, len(r.Images))
	for _, image := range r.Images {
		if strings.TrimSpace(image.Name) == "" || !digest.MatchString(image.Digest) {
			return fmt.Errorf("invalid image entry for %q", image.Name)
		}
		if _, exists := seen[image.Name]; exists {
			return fmt.Errorf("duplicate image name %q", image.Name)
		}
		seen[image.Name] = struct{}{}
	}
	if strings.TrimSpace(r.MigrationVersion) == "" {
		return errors.New("migration_version is required")
	}
	if strings.TrimSpace(r.ConfigSchemaVersion) == "" {
		return errors.New("config_schema_version is required")
	}
	if r.BuiltAt.IsZero() || r.BuiltAt.Location() != time.UTC {
		return errors.New("built_at must be an authoritative UTC timestamp")
	}
	if strings.TrimSpace(r.PreviousLKGRelease) == "" {
		return errors.New("previous_lkg_release_id is required")
	}
	return nil
}

// Canonical returns stable JSON so identical releases produce identical
// evidence hashes regardless of input image ordering.
func (r Release) Canonical() ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	sort.Slice(r.Images, func(i, j int) bool { return r.Images[i].Name < r.Images[j].Name })
	return json.MarshalIndent(r, "", "  ")
}

// Seal writes the manifest and checksum with create-exclusive semantics. A
// release identifier can therefore never be silently replaced in place.
func Seal(path string, release Release) (string, error) {
	body, err := release.Canonical()
	if err != nil {
		return "", err
	}
	body = append(body, '\n')
	sum := sha256.Sum256(body)
	encoded := hex.EncodeToString(sum[:])

	if _, err := os.Stat(path); err == nil {
		return "", fmt.Errorf("refusing to overwrite immutable manifest %q", path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	checksumPath := path + ".sha256"
	if _, err := os.Stat(checksumPath); err == nil {
		return "", fmt.Errorf("refusing to overwrite immutable checksum %q", checksumPath)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}

	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return "", err
	}
	if _, err = file.Write(body); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(path)
		return "", err
	}

	checksum, err := os.OpenFile(checksumPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		_ = os.Remove(path)
		return "", err
	}
	_, writeErr := fmt.Fprintf(checksum, "%s  %s\n", encoded, path)
	if closeErr := checksum.Close(); writeErr == nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		_ = os.Remove(path)
		_ = os.Remove(checksumPath)
		return "", writeErr
	}
	return encoded, nil
}
