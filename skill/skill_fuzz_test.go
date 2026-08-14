package skill

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func FuzzParseSkillFrontmatter(f *testing.F) {
	for _, seed := range []string{
		"",
		"---\n---\nbody",
		"---\r\nname: review\r\ndescription: Check changes\r\ndisable-model-invocation: false\r\n---\r\nInstructions\r\n",
		"---\rname: 'quoted value'\rdescription: \"line\\nvalue\"\rdisable-model-invocation: true\r---\rbody",
		"---\nname: duplicate\nname: again\n---\n",
		"---\ndescription: |\n---\n",
		"---\nname: 'unterminated\n---\n",
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, content string) {
		if len(content) > 32*1024 {
			t.Skip()
		}

		frontmatter, body, err := parseSkillFrontmatter(content)
		normalized := strings.ReplaceAll(strings.ReplaceAll(content, "\r\n", "\n"), "\r", "\n")
		normalizedFrontmatter, normalizedBody, normalizedErr := parseSkillFrontmatter(normalized)
		if !reflect.DeepEqual(frontmatter, normalizedFrontmatter) || body != normalizedBody || (err == nil) != (normalizedErr == nil) {
			t.Fatalf("newline normalization changed parse result:\noriginal:   frontmatter=%#v body=%q failed=%t\nnormalized: frontmatter=%#v body=%q failed=%t", frontmatter, body, err != nil, normalizedFrontmatter, normalizedBody, normalizedErr != nil)
		}
		if err != nil {
			return
		}
		if strings.ContainsRune(body, '\r') {
			t.Fatalf("successful parse returned unnormalized body %q", body)
		}

		canonical := fmt.Sprintf("---\nname: %s\ndescription: %s\ndisable-model-invocation: %t\n---\n%s", strconv.Quote(frontmatter.name), strconv.Quote(frontmatter.description), frontmatter.disableModelInvocation, body)
		canonicalFrontmatter, canonicalBody, canonicalErr := parseSkillFrontmatter(canonical)
		if canonicalErr != nil {
			t.Fatalf("canonical fields failed to parse: %v", canonicalErr)
		}
		if !reflect.DeepEqual(canonicalFrontmatter, frontmatter) || canonicalBody != body {
			t.Fatalf("canonical fields changed result: got frontmatter=%#v body=%q, want frontmatter=%#v body=%q", canonicalFrontmatter, canonicalBody, frontmatter, body)
		}
	})
}
