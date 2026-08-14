package agent

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/eul-ai/eul/skill"
)

func TestFormatSkillsForPromptUsesMetadataOnly(t *testing.T) {
	skills := []skill.Skill{
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
	skills, warnings := skill.Load(filepath.Dir(filepath.Dir(path)))
	if len(skills) != 1 || len(warnings) != 0 {
		t.Fatalf("skills = %+v, warnings = %v", skills, warnings)
	}
	writeTestSkill(t, path, "review", "Review code", false, "Updated body")

	expanded, err := expandSkillCommand(" /skill:review focus on tests ", skills)
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

	unknown, err := expandSkillCommand("/skill:unknown", skills)
	if err != nil || unknown != "/skill:unknown" {
		t.Fatalf("unknown expansion = %q, %v", unknown, err)
	}
}

func TestEngineExpandsSkillCommandWithoutMovingImage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "review", "SKILL.md")
	writeTestSkill(t, path, "review", "Review code", false, "Follow the review process.")
	skills, _ := skill.Load(filepath.Dir(filepath.Dir(path)))
	provider := &scriptedProvider{t: t, steps: []providerStep{
		func(_ context.Context, request Request, _ TextSink) (Response, error) {
			parts := request.Inputs[0].Content
			if len(parts) != 4 || parts[0].Text != "  " || !strings.Contains(parts[1].Text, "Follow the review process.") || parts[2].Kind != ContentPartImage || parts[2].Image == nil || parts[3].Text != " after" {
				t.Fatalf("content = %+v", parts)
			}
			return Response{Text: "done"}, nil
		},
	}}
	engine := newTestEngine(t, provider, &fakeToolbox{}, Options{Skills: skills})
	if _, err := engine.RunContent(context.Background(), []ContentPart{
		{Kind: ContentPartText, Text: "  "},
		{Kind: ContentPartText, Text: "/skill:review before"},
		{Kind: ContentPartImage, Image: &Image{MediaType: "image/png", Data: []byte("png")}},
		{Kind: ContentPartText, Text: " after"},
	}, discardEvents); err != nil {
		t.Fatal(err)
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
	skills, warnings := skill.Load(filepath.Dir(filepath.Dir(path)))
	if len(skills) != 1 || len(warnings) != 0 {
		t.Fatalf("skills = %+v, warnings = %v", skills, warnings)
	}

	provider := &scriptedProvider{t: t, steps: []providerStep{
		func(_ context.Context, request Request, _ TextSink) (Response, error) {
			if len(request.Inputs) != 1 || request.Inputs[0].Kind != InputUser {
				t.Fatalf("inputs = %+v", request.Inputs)
			}
			input := request.Inputs[0].PlainText()
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
