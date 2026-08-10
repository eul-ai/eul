package agent

import (
	"context"
	"html"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadSkillsDiscoversSkillFilesAndUsesDirectoryOrder(t *testing.T) {
	project := filepath.Join(t.TempDir(), "project-skills")
	global := filepath.Join(t.TempDir(), "global-skills")
	writeTestSkill(t, filepath.Join(project, "review", "SKILL.md"), "review", "Project review", false, "Project instructions")
	writeTestSkill(t, filepath.Join(global, "review", "SKILL.md"), "review", "Global review", false, "Global instructions")
	writeTestSkill(t, filepath.Join(global, "nested", "format", "SKILL.md"), "", "Format files", true, "Format instructions")
	writeTestSkill(t, filepath.Join(global, ".hidden", "hidden", "SKILL.md"), "hidden", "Hidden", false, "Hidden instructions")
	if err := os.MkdirAll(global, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(global, "root.md"), []byte("not a skill"), 0o644); err != nil {
		t.Fatal(err)
	}

	skills, warnings := LoadSkills(project, global)
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v", warnings)
	}
	if len(skills) != 2 {
		t.Fatalf("skills = %+v", skills)
	}
	if skills[0].Name != "review" || skills[0].Description != "Project review" {
		t.Fatalf("project skill did not win: %+v", skills[0])
	}
	if skills[1].Name != "format" || !skills[1].DisableModelInvocation {
		t.Fatalf("nested skill = %+v", skills[1])
	}
}

func TestLoadSkillsStopsAtSkillRoot(t *testing.T) {
	root := t.TempDir()
	writeTestSkill(t, filepath.Join(root, "bundle", "SKILL.md"), "bundle", "Bundle", false, "Bundle instructions")
	writeTestSkill(t, filepath.Join(root, "bundle", "nested", "SKILL.md"), "nested", "Nested", false, "Nested instructions")

	skills, warnings := LoadSkills(root)
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v", warnings)
	}
	if len(skills) != 1 || skills[0].Name != "bundle" {
		t.Fatalf("skills = %+v", skills)
	}
}

func TestParseSkillFrontmatterSupportsMinimalScalarSubset(t *testing.T) {
	content := "---\r\n# comment\r\nname: 'code-review'\r\ndescription: \"Review: code\\ncarefully\"\r\ndisable-model-invocation: true\r\nmetadata:\r\n  owner: team\r\n---\r\nInstructions.\r\n"
	frontmatter, body, err := parseSkillFrontmatter(content)
	if err != nil {
		t.Fatal(err)
	}
	if frontmatter.name != "code-review" || frontmatter.description != "Review: code\ncarefully" || !frontmatter.disableModelInvocation {
		t.Fatalf("frontmatter = %+v", frontmatter)
	}
	if body != "Instructions.\n" {
		t.Fatalf("body = %q", body)
	}
}

func TestParseSkillFrontmatterRejectsUnsupportedOrAmbiguousValues(t *testing.T) {
	for _, test := range []struct {
		name    string
		content string
		want    string
	}{
		{name: "block scalar", content: "---\ndescription: >\n  folded\n---\nbody", want: "block scalars"},
		{name: "collection", content: "---\ndescription: [one, two]\n---\nbody", want: "non-scalar"},
		{name: "duplicate", content: "---\ndescription: one\ndescription: two\n---\nbody", want: "duplicated"},
		{name: "boolean", content: "---\ndescription: valid\ndisable-model-invocation: yes\n---\nbody", want: "true or false"},
		{name: "delimiter", content: "description: valid\n---\nbody", want: "opening"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, _, err := parseSkillFrontmatter(test.content)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestLoadSkillsReportsMissingDescriptionAndLoadsValidSiblings(t *testing.T) {
	root := t.TempDir()
	missingPath := filepath.Join(root, "missing", "SKILL.md")
	writeTestSkill(t, missingPath, "missing", "", false, "Missing")
	writeTestSkill(t, filepath.Join(root, "invalid", "SKILL.md"), "Invalid Name", "Present", false, "Present")

	skills, warnings := LoadSkills(root)
	if len(skills) != 1 || skills[0].Name != "Invalid Name" {
		t.Fatalf("skills = %+v", skills)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], filepath.ToSlash(missingPath)) || !strings.Contains(warnings[0], "description is empty") {
		t.Fatalf("warnings = %v", warnings)
	}
}

func TestLoadSkillsIgnoresMissingDirectoriesWithoutWarning(t *testing.T) {
	skills, warnings := LoadSkills(filepath.Join(t.TempDir(), "missing"))
	if len(skills) != 0 || len(warnings) != 0 {
		t.Fatalf("skills = %+v, warnings = %v", skills, warnings)
	}
}

func TestFormatSkillsForPromptUsesMetadataOnly(t *testing.T) {
	skills := []Skill{
		{Name: "review", Description: `Review <code> & "tests".`, FilePath: "/skills/review/SKILL.md"},
		{Name: "manual", Description: "Manual only", FilePath: "/skills/manual/SKILL.md", DisableModelInvocation: true},
	}

	prompt := formatSkillsForPrompt(skills)
	for _, want := range []string{"<available_skills>", "<name>review</name>", "&lt;code&gt;", "&amp;", "/skills/review/SKILL.md"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt omits %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "manual") {
		t.Fatalf("prompt includes disabled skill:\n%s", prompt)
	}
}

func TestExpandSkillCommandLoadsCurrentBodyAndArguments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "review&tools", "SKILL.md")
	writeTestSkill(t, path, "review", "Review code", false, "Original body")
	skills, warnings := LoadSkills(filepath.Dir(filepath.Dir(path)))
	if len(skills) != 1 || len(warnings) != 0 {
		t.Fatalf("skills = %+v, warnings = %v", skills, warnings)
	}
	writeTestSkill(t, path, "review", "Review code", false, "Updated body")

	expanded, err := expandSkillCommand(" /skill:review focus on tests ", map[string]Skill{"review": skills[0]})
	if err != nil {
		t.Fatal(err)
	}
	baseDir := html.EscapeString(filepath.ToSlash(filepath.Dir(path)))
	for _, want := range []string{`<skill name="review" location="`, "References are relative to " + baseDir, "Updated body", "</skill>\n\nfocus on tests"} {
		if !strings.Contains(expanded, want) {
			t.Fatalf("expanded prompt omits %q:\n%s", want, expanded)
		}
	}
	if strings.Contains(expanded, "description:") {
		t.Fatalf("expanded prompt includes frontmatter:\n%s", expanded)
	}

	unknown, err := expandSkillCommand("/skill:unknown", map[string]Skill{"review": skills[0]})
	if err != nil || unknown != "/skill:unknown" {
		t.Fatalf("unknown expansion = %q, %v", unknown, err)
	}
}

func writeTestSkill(t *testing.T, path, name, description string, disabled bool, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	var content strings.Builder
	content.WriteString("---\n")
	if name != "" {
		content.WriteString("name: ")
		content.WriteString(name)
		content.WriteByte('\n')
	}
	content.WriteString("description: ")
	content.WriteString(description)
	content.WriteByte('\n')
	if disabled {
		content.WriteString("disable-model-invocation: true\n")
	}
	content.WriteString("---\n")
	content.WriteString(body)
	content.WriteByte('\n')
	if err := os.WriteFile(path, []byte(content.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestEngineExpandsSkillCommandsBeforeProviderRequest(t *testing.T) {
	path := filepath.Join(t.TempDir(), "review", "SKILL.md")
	writeTestSkill(t, path, "review", "Review code", false, "Follow the review process.")
	skills, warnings := LoadSkills(filepath.Dir(filepath.Dir(path)))
	if len(skills) != 1 || len(warnings) != 0 {
		t.Fatalf("skills = %+v, warnings = %v", skills, warnings)
	}

	provider := &scriptedProvider{t: t, steps: []providerStep{
		func(_ context.Context, request Request, _ TextSink) (Response, error) {
			if len(request.Inputs) != 1 || request.Inputs[0].Kind != InputUser {
				t.Fatalf("inputs = %+v", request.Inputs)
			}
			input := request.Inputs[0].Text
			if !strings.Contains(input, "Follow the review process.") || !strings.HasSuffix(input, "</skill>\n\ncheck tests") {
				t.Fatalf("skill input = %q", input)
			}
			return Response{Text: "done"}, nil
		},
	}}
	engine := newTestEngine(t, provider, &fakeToolbox{definitions: []ToolDefinition{{Name: "read"}}}, Options{Skills: skills})
	if _, err := engine.Run(context.Background(), "/skill:review check tests", discardEvents); err != nil {
		t.Fatal(err)
	}
}
