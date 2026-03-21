package tscheck

import (
	"go/types"
	"strings"
	"testing"
	"text/template/parse"

	"github.com/tooolbox/check/tsextract"
)

func TestCheckTypeError(t *testing.T) {
	// Template with a deliberate type error: .Naem instead of .Name.
	const templateText = `<h1>{{.Title}}</h1>
<script>
  const item: { Name: string; Price: number } = (undefined! as { Name: string; Price: number });
  console.log(item.Naem);
</script>`

	trees, err := parse.Parse("index.gohtml", templateText, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	tree := trees["index.gohtml"]

	// We don't need real action types for this test since the script block
	// has hardcoded types. We just need Extract to find the script block.
	actionTypes := make(map[*parse.ActionNode]tsextract.ActionTypes)
	for _, node := range tree.Root.Nodes {
		if a, ok := node.(*parse.ActionNode); ok {
			if strings.Contains(a.String(), ".Title") {
				actionTypes[a] = tsextract.ActionTypes{
					InputType:    types.Typ[types.String],
					ResolvedType: types.Typ[types.String],
				}
			}
		}
	}

	result, err := tsextract.Extract(tree, actionTypes)
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || len(result.ScriptBlocks) != 1 {
		t.Fatalf("expected 1 script block, got %v", result)
	}

	// Format the .ts file.
	content := tsextract.FormatTSFile("index.gohtml", "/templates/index.gohtml", result)
	t.Logf("Generated .ts:\n%s", content)

	// Run the type checker.
	checkResult, err := Check([]FileEntry{{
		VirtualPath: "/src/index.gohtml.ts",
		Content:     content,
		Result:      result,
	}})
	if err != nil {
		t.Fatal(err)
	}

	// We expect at least one diagnostic about "Naem" not existing.
	if len(checkResult.Diagnostics) == 0 {
		t.Fatal("expected at least one diagnostic, got none")
	}

	found := false
	for _, d := range checkResult.Diagnostics {
		t.Logf("Diagnostic: %s:%d:%d [TS%d] %s", d.TemplateFile, d.Line, d.Col, d.Code, d.Message)
		if strings.Contains(d.Message, "Naem") || d.Code == 2339 {
			found = true
			if d.Category != "error" {
				t.Errorf("expected category 'error', got %q", d.Category)
			}
			// The diagnostic should point to the template file.
			if d.TemplateFile == "" {
				t.Error("expected non-empty TemplateFile")
			}
			// Line should be within the template (lines 3-5 are the script block).
			if d.Line < 3 || d.Line > 5 {
				t.Errorf("expected line in template script block (3-5), got %d", d.Line)
			}
		}
	}
	if !found {
		t.Error("expected a diagnostic about 'Naem' (TS2339), not found")
	}
}

func TestCheckNoErrors(t *testing.T) {
	// Template with correct types — should produce no diagnostics.
	const templateText = `<script>
  const title: string = (undefined! as string);
  console.log(title.toUpperCase());
</script>`

	trees, err := parse.Parse("ok.gohtml", templateText, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	tree := trees["ok.gohtml"]

	result, err := tsextract.Extract(tree, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	content := tsextract.FormatTSFile("ok.gohtml", "/templates/ok.gohtml", result)
	t.Logf("Generated .ts:\n%s", content)

	checkResult, err := Check([]FileEntry{{
		VirtualPath: "/src/ok.gohtml.ts",
		Content:     content,
		Result:      result,
	}})
	if err != nil {
		t.Fatal(err)
	}

	if len(checkResult.Diagnostics) > 0 {
		for _, d := range checkResult.Diagnostics {
			t.Errorf("unexpected diagnostic: %s:%d:%d [TS%d] %s", d.TemplateFile, d.Line, d.Col, d.Code, d.Message)
		}
	}
}

func TestCheckEmpty(t *testing.T) {
	result, err := Check(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Diagnostics) != 0 {
		t.Errorf("expected no diagnostics for empty input, got %d", len(result.Diagnostics))
	}
}
