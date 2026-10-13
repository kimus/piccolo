package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	picintegration "github.com/kimus/piccolo/internal/integration"
	"github.com/kimus/piccolo/internal/runtimeimage"
)

type releaseFetcher interface {
	Fetch(context.Context, picintegration.PackageRef) (fetchedRelease, error)
}

type fetchedRelease struct {
	Ref      picintegration.PackageRef
	Version  string
	Digest   string
	Bundle   runtimeimage.IntegrationBundle
	Manifest picintegration.ReleaseManifest
}

var newReleaseFetcher = func() releaseFetcher { return githubReleaseFetcher{client: http.DefaultClient} }

type githubReleaseFetcher struct{ client *http.Client }

func (g githubReleaseFetcher) Fetch(ctx context.Context, ref picintegration.PackageRef) (fetchedRelease, error) {
	if ref.Provider != "github" {
		return fetchedRelease{}, fmt.Errorf("unsupported integration provider %q", ref.Provider)
	}
	tag := ref.Selector
	if tag == "" || tag == "latest" {
		latest, err := g.githubJSON(ctx, fmt.Sprintf("https://api.github.com/repos/%s/%s/releases/latest", ref.Owner, ref.Repo))
		if err != nil {
			return fetchedRelease{}, err
		}
		var obj struct {
			TagName string `json:"tag_name"`
		}
		if err := json.Unmarshal(latest, &obj); err != nil {
			return fetchedRelease{}, fmt.Errorf("parse GitHub latest release: %w", err)
		}
		if strings.TrimSpace(obj.TagName) == "" {
			return fetchedRelease{}, fmt.Errorf("GitHub latest release for %s/%s did not include tag_name", ref.Owner, ref.Repo)
		}
		tag = obj.TagName
	}
	base := fmt.Sprintf("https://github.com/%s/%s/releases/download/%s", ref.Owner, ref.Repo, tag)
	manifestBytes, err := g.httpGet(ctx, base+"/piccolo-integration-release.json")
	if err != nil {
		return fetchedRelease{}, fmt.Errorf("download release manifest for %s@%s: %w", ref.Short(), tag, err)
	}
	manifest, err := picintegration.ParseReleaseManifest(manifestBytes)
	if err != nil {
		return fetchedRelease{}, err
	}
	if manifest.Package != ref.Package() {
		return fetchedRelease{}, fmt.Errorf("release manifest package %q does not match %q", manifest.Package, ref.Package())
	}
	platform := "linux/" + runtime.GOARCH
	artifact, ok := manifest.ArtifactFor(platform)
	if !ok {
		return fetchedRelease{}, fmt.Errorf("release %s@%s has no artifact for %s", ref.Short(), tag, platform)
	}
	archive, err := g.httpGet(ctx, base+"/"+artifact.Asset)
	if err != nil {
		return fetchedRelease{}, fmt.Errorf("download release artifact %s: %w", artifact.Asset, err)
	}
	if err := picintegration.VerifySHA256(archive, artifact.SHA256); err != nil {
		return fetchedRelease{}, err
	}
	root, commands, err := extractIntegrationArchive(archive)
	if err != nil {
		return fetchedRelease{}, err
	}
	return fetchedRelease{Ref: ref, Version: manifest.Version, Digest: "sha256:" + strings.TrimPrefix(strings.ToLower(artifact.SHA256), "sha256:"), Manifest: manifest, Bundle: runtimeimage.IntegrationBundle{Package: ref.Package(), Version: manifest.Version, Root: root, Commands: commands}}, nil
}

func (g githubReleaseFetcher) githubJSON(ctx context.Context, url string) ([]byte, error) {
	return g.httpGetWithAccept(ctx, url, "application/vnd.github+json")
}

func (g githubReleaseFetcher) httpGet(ctx context.Context, url string) ([]byte, error) {
	return g.httpGetWithAccept(ctx, url, "application/octet-stream")
}

func (g githubReleaseFetcher) httpGetWithAccept(ctx context.Context, url, accept string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if tok := strings.TrimSpace(os.Getenv("GITHUB_TOKEN")); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	req.Header.Set("Accept", accept)
	res, err := g.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusForbidden && res.Header.Get("X-RateLimit-Remaining") == "0" {
		return nil, fmt.Errorf("GitHub API rate limit exceeded; set GITHUB_TOKEN and retry")
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("GET %s: %s", url, res.Status)
	}
	return io.ReadAll(res.Body)
}

func extractIntegrationArchive(data []byte) (string, []string, error) {
	root, err := os.MkdirTemp("", "piccolo-integration-bundle-*")
	if err != nil {
		return "", nil, err
	}
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		_ = os.RemoveAll(root)
		return "", nil, fmt.Errorf("open integration archive: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			_ = os.RemoveAll(root)
			return "", nil, err
		}
		clean := filepath.Clean(hdr.Name)
		if clean == "." || strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
			_ = os.RemoveAll(root)
			return "", nil, fmt.Errorf("unsafe path in integration archive: %s", hdr.Name)
		}
		path := filepath.Join(root, clean)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(path, 0o755); err != nil {
				return "", nil, err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return "", nil, err
			}
			f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode).Perm())
			if err != nil {
				return "", nil, err
			}
			_, copyErr := io.Copy(f, tr)
			closeErr := f.Close()
			if copyErr != nil {
				return "", nil, copyErr
			}
			if closeErr != nil {
				return "", nil, closeErr
			}
		}
	}
	binDir := filepath.Join(root, "bin")
	entries, err := os.ReadDir(binDir)
	if err != nil {
		_ = os.RemoveAll(root)
		return "", nil, fmt.Errorf("integration archive missing bin/: %w", err)
	}
	var commands []string
	for _, e := range entries {
		if !e.IsDir() {
			commands = append(commands, e.Name())
		}
	}
	if len(commands) == 0 {
		_ = os.RemoveAll(root)
		return "", nil, fmt.Errorf("integration archive contains no bin commands")
	}
	return root, commands, nil
}
