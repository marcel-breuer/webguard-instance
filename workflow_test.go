package webguardinstance_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDockerImageWorkflowGeneratesChangelogOnTag(t *testing.T) {
	workflowPath := filepath.Join(".github", "workflows", "docker-image.yml")
	content, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatalf("read docker image workflow: %v", err)
	}

	workflow := string(content)
	requiredSnippets := []string{
		`tags:
      - "v*"`,
		"release:",
		"name: Generate Changelog",
		"if: startsWith(github.ref, 'refs/tags/')",
		"contents: write",
		"ref: ${{ github.event.repository.default_branch }}",
		"fetch-depth: 0",
		`repos/${GITHUB_REPOSITORY}/releases/generate-notes`,
		"--field tag_name=\"$GITHUB_REF_NAME\"",
		"bash .github/scripts/update-changelog.sh RELEASE_NOTES.md",
		"git add CHANGELOG.md",
		`git commit -m "docs: update changelog for ${GITHUB_REF_NAME}"`,
		`git push origin "HEAD:${{ github.event.repository.default_branch }}"`,
		`gh release create "$GITHUB_REF_NAME" \`,
		"--notes-file RELEASE_NOTES.md",
		`gh release edit "$GITHUB_REF_NAME" \`,
		"--verify-tag",
	}

	for _, snippet := range requiredSnippets {
		if !strings.Contains(workflow, snippet) {
			t.Fatalf("docker image workflow should contain %q", snippet)
		}
	}

	forbiddenSnippets := []string{
		"needs: publish",
		"Release $GITHUB_REF_NAME already exists; skipping changelog generation.",
		"--generate-notes",
	}

	for _, snippet := range forbiddenSnippets {
		if strings.Contains(workflow, snippet) {
			t.Fatalf("docker image workflow should not contain %q", snippet)
		}
	}
}

func TestUpdateChangelogScriptReplacesExistingTagEntry(t *testing.T) {
	tempDir := t.TempDir()
	scriptPath := filepath.Join(".github", "scripts", "update-changelog.sh")
	notesPath := filepath.Join(tempDir, "RELEASE_NOTES.md")
	changelogPath := filepath.Join(tempDir, "CHANGELOG.md")

	if err := os.WriteFile(notesPath, []byte("### What's Changed\n\n* new generated note\n"), 0o600); err != nil {
		t.Fatalf("write release notes: %v", err)
	}

	existing := strings.Join([]string{
		"# Changelog",
		"",
		"## v1.2.3 - 2026-01-01",
		"",
		"* stale note",
		"",
		"## v1.2.2 - 2025-12-31",
		"",
		"* previous note",
		"",
	}, "\n")
	if err := os.WriteFile(changelogPath, []byte(existing), 0o600); err != nil {
		t.Fatalf("write existing changelog: %v", err)
	}

	cmd := exec.Command("bash", scriptPath, notesPath)
	cmd.Env = append(os.Environ(),
		"GITHUB_REF_NAME=v1.2.3",
		"CHANGELOG_DATE=2026-05-14",
		"CHANGELOG_FILE="+changelogPath,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("update changelog failed: %v\n%s", err, output)
	}

	contentBytes, err := os.ReadFile(changelogPath)
	if err != nil {
		t.Fatalf("read updated changelog: %v", err)
	}
	content := string(contentBytes)

	requiredSnippets := []string{
		"# Changelog\n\n## v1.2.3 - 2026-05-14",
		"### What's Changed\n\n* new generated note",
		"## v1.2.2 - 2025-12-31\n\n* previous note",
	}
	for _, snippet := range requiredSnippets {
		if !strings.Contains(content, snippet) {
			t.Fatalf("updated changelog should contain %q, got:\n%s", snippet, content)
		}
	}

	if strings.Contains(content, "* stale note") {
		t.Fatalf("updated changelog should replace the stale tag entry, got:\n%s", content)
	}
}
