package skill

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestPromptSectionUsesMetadataOnly(t *testing.T) {
	skills := []Skill{
		{Name: "review", Description: `Review <code> & "tests".`, FilePath: "/skills/review/SKILL.md"},
		{Name: "manual", Description: "Manual only", FilePath: "/skills/manual/SKILL.md", DisableModelInvocation: true},
	}

	prompt := PromptSection(skills)
	for _, want := range []string{"<available_skills>", "<name>review</name>", "&lt;code&gt;", "&amp;", "/skills/review/SKILL.md"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt omits %q:\n%s", want, prompt)
		}
	}
	if strings.Contains(prompt, "manual") {
		t.Fatalf("prompt includes disabled skill:\n%s", prompt)
	}
}

func TestExpandLoadsCurrentBodyAndArguments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "review&tools", "SKILL.md")
	writeTestSkill(t, path, "review", "Review code", false, "Original body")
	skills, warnings := Load(filepath.Dir(filepath.Dir(path)))
	if len(skills) != 1 || len(warnings) != 0 {
		t.Fatalf("skills = %+v, warnings = %v", skills, warnings)
	}
	writeTestSkill(t, path, "review", "Review code", false, "Updated body")

	expanded, err := Expand(" /skill:review focus on tests ", skills)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`<skill name="review" location="`, strings.ReplaceAll(filepath.ToSlash(path), "&", "&amp;"), "Updated body", "</skill>\n\nfocus on tests"} {
		if !strings.Contains(expanded, want) {
			t.Fatalf("expanded prompt omits %q:\n%s", want, expanded)
		}
	}
	if strings.Contains(expanded, "description:") {
		t.Fatalf("expanded prompt includes frontmatter:\n%s", expanded)
	}

	unknown, err := Expand("/skill:unknown", skills)
	if err != nil || unknown != "/skill:unknown" {
		t.Fatalf("unknown expansion = %q, %v", unknown, err)
	}
}
