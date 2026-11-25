package openclaw

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"go.autonomous.ai/os/system/skills"
)

const (
	// defaultPluginsSkillsRoot is used when the OTA metadata URL gives no
	// usable host, and as the fallback when that host fails.
	defaultPluginsSkillsRoot = "https://storage.googleapis.com/s3-autonomous-upgrade-3/plugins-skills"

	// roleSkillsDir holds one <role>/skills.zip per role under the root.
	roleSkillsDir = "openclaw-roles"

	roleSkillsZipPrefix  = "skills/"
	roleSkillsMaxRetries = 3
	roleSkillsRetryDelay = 2 * time.Second

	// mcpSkillsDir holds one <name>.zip per MCP connector skill under the root.
	mcpSkillsDir = "skills_for_MCP"

	otaMetadataSuffix = "/ota/metadata.json"
)

// pluginsSkillsRoots returns the plugin-skill roots to try, in order: the host
// that serves this device's OTA metadata first (the release root is the parent
// of the "/<prefix>/ota/metadata.json" path), then the default root. Only an
// https metadata URL is used; any other leaves just the default root.
func pluginsSkillsRoots(otaMetadataURL string) []string {
	roots := []string{}
	if u, err := url.Parse(strings.TrimSpace(otaMetadataURL)); err == nil &&
		u.Scheme == "https" && u.Host != "" &&
		strings.HasSuffix(u.Path, otaMetadataSuffix) {
		prefix := path.Dir(strings.TrimSuffix(u.Path, otaMetadataSuffix))
		root := u.Scheme + "://" + u.Host + strings.TrimSuffix(prefix, "/") + "/plugins-skills"
		if root != defaultPluginsSkillsRoot {
			roots = append(roots, root)
		}
	}
	return append(roots, defaultPluginsSkillsRoot)
}

// downloadFirst downloads the first URL that answers, retrying the whole list
// up to roleSkillsMaxRetries times, and returns the temp file path.
func downloadFirst(urls []string, pattern, logPrefix, name string) (string, error) {
	var lastErr error
	for attempt := 1; attempt <= roleSkillsMaxRetries; attempt++ {
		for _, u := range urls {
			p, err := skills.DownloadToTempFile(u, pattern)
			if err == nil {
				return p, nil
			}
			lastErr = err
			slog.Warn(logPrefix+" zip download failed", "component", "openclaw", "name", name, "url", u, "attempt", attempt, "error", err)
		}
		if attempt < roleSkillsMaxRetries {
			time.Sleep(roleSkillsRetryDelay)
		}
	}
	return "", lastErr
}

// ErrInvalidRole is returned when the role slug has an unsafe shape (empty or containing path-escaping characters).
var ErrInvalidRole = errors.New("invalid role")

// roleNamePattern allows only lowercase letters, digits, dash and underscore.
var roleNamePattern = regexp.MustCompile(`^[a-z0-9_-]+$`)

// InstallRoleSkills downloads <role>/skills.zip from the release host (see
// pluginsSkillsRoots) and extracts its `skills/` tree into
// {configDir}/workspace/skills, returning the number of files written.
func InstallRoleSkills(configDir, otaMetadataURL, role string) (int, error) {
	if !roleNamePattern.MatchString(role) {
		return 0, fmt.Errorf("%w: %q", ErrInvalidRole, role)
	}

	var urls []string
	for _, root := range pluginsSkillsRoots(otaMetadataURL) {
		urls = append(urls, fmt.Sprintf("%s/%s/%s/skills.zip", root, roleSkillsDir, role))
	}
	tmpZip, err := downloadFirst(urls, "role-skills-*.zip", "[role-skills]", role)
	if err != nil {
		return 0, fmt.Errorf("download %s skills zip after %d retries: %w", role, roleSkillsMaxRetries, err)
	}
	defer os.Remove(tmpZip)

	skillsDir := filepath.Join(configDir, "workspace", "skills")
	count, err := extractDirFromZip(tmpZip, roleSkillsZipPrefix, skillsDir)
	if err != nil {
		return count, fmt.Errorf("extract %s skills: %w", role, err)
	}
	slog.Info("[role-skills] installed role", "component", "openclaw", "role", role, "files", count, "dir", skillsDir)
	return count, nil
}

// EnsureMCPSkill makes sure the MCP connector skill <name> is present under {configDir}/workspace/skills/<name>.
func EnsureMCPSkill(configDir, otaMetadataURL, name string) error {
	if !roleNamePattern.MatchString(name) {
		return fmt.Errorf("%w: %q", ErrInvalidRole, name)
	}

	skillsDir := filepath.Join(configDir, "workspace", "skills")
	if _, err := os.Stat(filepath.Join(skillsDir, name, "SKILL.md")); err == nil {
		return nil
	}

	var urls []string
	for _, root := range pluginsSkillsRoots(otaMetadataURL) {
		urls = append(urls, fmt.Sprintf("%s/%s/%s.zip", root, mcpSkillsDir, name))
	}
	tmpZip, err := downloadFirst(urls, "mcp-skill-*.zip", "[mcp-skill]", name)
	if err != nil {
		return fmt.Errorf("download %s mcp skill after %d retries: %w", name, roleSkillsMaxRetries, err)
	}
	defer os.Remove(tmpZip)

	count, err := extractDirFromZip(tmpZip, "", skillsDir)
	if err != nil {
		return fmt.Errorf("extract %s mcp skill: %w", name, err)
	}
	slog.Info("[mcp-skill] installed", "component", "openclaw", "name", name, "files", count, "dir", skillsDir)
	return nil
}

// extractDirFromZip extracts every entry under srcPrefix in the zip at zipPath into destDir (with the prefix stripped), returning the number of files written.
func extractDirFromZip(zipPath, srcPrefix, destDir string) (int, error) {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return 0, fmt.Errorf("open zip %s: %w", zipPath, err)
	}
	defer r.Close()

	cleanDest, err := filepath.Abs(destDir)
	if err != nil {
		return 0, fmt.Errorf("abs dest: %w", err)
	}
	cleanDest = filepath.Clean(cleanDest) + string(os.PathSeparator)

	count := 0
	for _, f := range r.File {
		if !strings.HasPrefix(f.Name, srcPrefix) {
			continue
		}
		rel := strings.TrimPrefix(f.Name, srcPrefix)
		if rel == "" {
			continue
		}
		if filepath.IsAbs(rel) || strings.Contains(rel, "..") {
			return count, fmt.Errorf("invalid zip entry %q", f.Name)
		}
		target := filepath.Join(destDir, rel)
		absTarget, err := filepath.Abs(target)
		if err != nil {
			return count, fmt.Errorf("abs target %s: %w", target, err)
		}
		if !strings.HasPrefix(absTarget+string(os.PathSeparator), cleanDest) &&
			absTarget+string(os.PathSeparator) != cleanDest {
			return count, fmt.Errorf("zip entry escapes dest: %q", f.Name)
		}

		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0755); err != nil {
				return count, fmt.Errorf("mkdir %s: %w", target, err)
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
			return count, fmt.Errorf("mkdir parent %s: %w", target, err)
		}

		rc, err := f.Open()
		if err != nil {
			return count, fmt.Errorf("open zip entry %s: %w", f.Name, err)
		}
		out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
		if err != nil {
			rc.Close()
			return count, fmt.Errorf("create %s: %w", target, err)
		}
		if _, err := io.Copy(out, rc); err != nil {
			rc.Close()
			out.Close()
			return count, fmt.Errorf("write %s: %w", target, err)
		}
		rc.Close()
		if err := out.Close(); err != nil {
			return count, fmt.Errorf("close %s: %w", target, err)
		}
		count++
	}
	return count, nil
}
