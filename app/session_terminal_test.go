package app

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/eul-ai/eul/agent"
	"github.com/eul-ai/eul/skill"
)

func TestExpandSkillContentWithoutMovingImage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "review", "SKILL.md")
	writeConfigTestSkill(t, path, "review", "Review code")
	skills, _ := skill.Load(filepath.Dir(filepath.Dir(path)))

	content, err := expandSkillContent([]agent.ContentPart{
		{Kind: agent.ContentPartText, Text: "  "},
		{Kind: agent.ContentPartText, Text: "/skill:review check tests"},
		{Kind: agent.ContentPartImage, Image: &agent.Image{MediaType: "image/png", Data: []byte("png")}},
		{Kind: agent.ContentPartText, Text: " after"},
	}, skills)
	if err != nil {
		t.Fatal(err)
	}
	if len(content) != 4 || content[0].Text != "  " || !strings.Contains(content[1].Text, "Instructions") || !strings.HasSuffix(content[1].Text, "</skill>\n\ncheck tests") || content[2].Kind != agent.ContentPartImage || content[3].Text != " after" {
		t.Fatalf("content = %+v", content)
	}
}
