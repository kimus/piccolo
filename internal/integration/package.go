package integration

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

const ReleaseManifestSchemaV1 = "piccolo.integration.release.v1"

type SourceKind string

const (
	SourceRelease SourceKind = "release"
	SourceBuild   SourceKind = "build"
)

type Channel string

const (
	ChannelLatest Channel = "latest"
	ChannelFixed  Channel = "fixed"
)

var packagePartRE = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// PackageRef identifies an installable integration package. Selector is
// provider-owned and intentionally opaque; Piccolo does not normalize tag
// schemes such as v-prefixed versions.
type PackageRef struct {
	Provider  string
	Owner     string
	Repo      string
	Selector  string
	Requested string
}

func ParsePackageRef(input string) (PackageRef, error) {
	requested := strings.TrimSpace(input)
	if requested == "" {
		return PackageRef{}, fmt.Errorf("integration package reference is required")
	}
	provider := "github"
	rest := requested
	if i := strings.Index(rest, ":"); i >= 0 {
		provider = strings.TrimSpace(rest[:i])
		rest = strings.TrimSpace(rest[i+1:])
	}
	selector := ""
	if i := strings.LastIndex(rest, "@"); i >= 0 {
		selector = strings.TrimSpace(rest[i+1:])
		rest = strings.TrimSpace(rest[:i])
		if selector == "" {
			return PackageRef{}, fmt.Errorf("integration package selector is empty in %q", input)
		}
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 2 {
		return PackageRef{}, fmt.Errorf("unknown integration %q (use package refs like kimus/piccolo-gws or github:kimus/piccolo-gws@3.1.0)", input)
	}
	owner, repo := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
	if provider != "github" {
		return PackageRef{}, fmt.Errorf("unsupported integration provider %q (only github is supported)", provider)
	}
	for label, value := range map[string]string{"owner": owner, "repo": repo} {
		if !packagePartRE.MatchString(value) {
			return PackageRef{}, fmt.Errorf("invalid integration package %s %q", label, value)
		}
	}
	return PackageRef{Provider: provider, Owner: owner, Repo: repo, Selector: selector, Requested: requested}, nil
}

func (r PackageRef) Package() string   { return r.Provider + ":" + r.Owner + "/" + r.Repo }
func (r PackageRef) Short() string     { return r.Owner + "/" + r.Repo }
func (r PackageRef) StatePath() string { return "integrations/" + r.Owner + "/" + r.Repo }
func (r PackageRef) ContainerStatePath() string {
	return "/root/.piccolo/integrations/" + r.Owner + "/" + r.Repo
}
func (r PackageRef) Channel() Channel {
	if strings.TrimSpace(r.Selector) == "" || r.Selector == "latest" {
		return ChannelLatest
	}
	return ChannelFixed
}
func (r PackageRef) EffectiveSelector() string {
	if strings.TrimSpace(r.Selector) == "" {
		return "latest"
	}
	return r.Selector
}
func (r PackageRef) DescriptorCommand() []string { return []string{r.Repo, "describe", "--json"} }

// ReleaseManifest is the provider-published asset Piccolo reads before
// downloading a platform-specific integration bundle.
type ReleaseManifest struct {
	Schema    string            `json:"schema"`
	Package   string            `json:"package"`
	Version   string            `json:"version"`
	Artifacts []ReleaseArtifact `json:"artifacts"`
}

type ReleaseArtifact struct {
	Platform string `json:"platform"`
	Asset    string `json:"asset"`
	SHA256   string `json:"sha256"`
}

func ParseReleaseManifest(data []byte) (ReleaseManifest, error) {
	var m ReleaseManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return ReleaseManifest{}, fmt.Errorf("parse release manifest: %w", err)
	}
	if m.Schema != ReleaseManifestSchemaV1 {
		return ReleaseManifest{}, fmt.Errorf("unsupported release manifest schema %q (want %q)", m.Schema, ReleaseManifestSchemaV1)
	}
	if strings.TrimSpace(m.Package) == "" || strings.TrimSpace(m.Version) == "" {
		return ReleaseManifest{}, fmt.Errorf("release manifest package and version are required")
	}
	if len(m.Artifacts) == 0 {
		return ReleaseManifest{}, fmt.Errorf("release manifest contains no artifacts")
	}
	for i, a := range m.Artifacts {
		if strings.TrimSpace(a.Platform) == "" || strings.TrimSpace(a.Asset) == "" || strings.TrimSpace(a.SHA256) == "" {
			return ReleaseManifest{}, fmt.Errorf("release manifest artifact %d is incomplete", i)
		}
	}
	return m, nil
}

func (m ReleaseManifest) ArtifactFor(platform string) (ReleaseArtifact, bool) {
	for _, a := range m.Artifacts {
		if a.Platform == platform {
			return a, true
		}
	}
	return ReleaseArtifact{}, false
}

func VerifySHA256(data []byte, want string) error {
	sum := sha256.Sum256(data)
	got := hex.EncodeToString(sum[:])
	want = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(want)), "sha256:")
	if got != want {
		return fmt.Errorf("artifact digest mismatch: got sha256:%s, want sha256:%s", got, want)
	}
	return nil
}
