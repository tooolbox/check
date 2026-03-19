package tsextract

import (
	"go/types"
	"strings"
	"testing"
	"text/template/parse"
)

// helper to build ActionTypes where input == resolved (no pipe chain).
func scalarAction(typ types.Type) ActionTypes {
	return ActionTypes{InputType: typ, ResolvedType: typ}
}

func TestExtractScriptBlocks(t *testing.T) {
	const templateText = `<h1>{{.Title}}</h1>
<script>
  const items = {{.Items}};
  items.forEach(item => {
    console.log(item.Name);
  });
</script>
<p>footer</p>`

	trees, err := parse.Parse("test.gohtml", templateText, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	tree := trees["test.gohtml"]

	itemsType := types.NewSlice(types.NewStruct(
		[]*types.Var{
			types.NewField(0, nil, "Name", types.Typ[types.String], false),
			types.NewField(0, nil, "Price", types.Typ[types.Float64], false),
		},
		[]string{"", ""},
	))

	actionTypes := make(map[*parse.ActionNode]ActionTypes)
	for _, node := range tree.Root.Nodes {
		if a, ok := node.(*parse.ActionNode); ok {
			pipeStr := a.String()
			switch {
			case strings.Contains(pipeStr, ".Title"):
				actionTypes[a] = scalarAction(types.Typ[types.String])
			case strings.Contains(pipeStr, ".Items"):
				actionTypes[a] = scalarAction(itemsType)
			}
		}
	}

	blocks, err := Extract(tree, actionTypes)
	if err != nil {
		t.Fatal(err)
	}

	if len(blocks) != 1 {
		t.Fatalf("expected 1 script block, got %d", len(blocks))
	}

	block := blocks[0]
	if !strings.Contains(block.TypeScript, `(undefined! as Array<{ Name: string; Price: number }>)`) {
		t.Errorf("expected Items placeholder, got:\n%s", block.TypeScript)
	}
	if strings.Contains(block.TypeScript, "{{") {
		t.Errorf("template actions should not appear in output:\n%s", block.TypeScript)
	}
	if strings.Contains(block.TypeScript, "__TSEXTRACT_") {
		t.Errorf("placeholders should not appear in output:\n%s", block.TypeScript)
	}
	// Non-scalar type without JSON.parse should produce a warning.
	if len(block.Warnings) == 0 {
		t.Error("expected W008 warning for non-scalar type without JSON.parse")
	} else if !strings.Contains(block.Warnings[0], "W008") {
		t.Errorf("expected W008 warning, got: %s", block.Warnings[0])
	}
	t.Logf("Generated TypeScript:\n%s", block.TypeScript)
}

func TestExtractWithControlFlow(t *testing.T) {
	const templateText = `<script>
{{if .Debug}}
  console.log("debug:", {{.DebugInfo}});
{{end}}
  const title = {{.Title}};
</script>`

	trees, err := parse.Parse("test.gohtml", templateText, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	tree := trees["test.gohtml"]

	actionTypes := make(map[*parse.ActionNode]ActionTypes)
	var findActions func([]parse.Node)
	findActions = func(nodes []parse.Node) {
		for _, node := range nodes {
			switch n := node.(type) {
			case *parse.ActionNode:
				pipeStr := n.String()
				switch {
				case strings.Contains(pipeStr, ".DebugInfo"):
					actionTypes[n] = scalarAction(types.Typ[types.String])
				case strings.Contains(pipeStr, ".Title"):
					actionTypes[n] = scalarAction(types.Typ[types.String])
				}
			case *parse.IfNode:
				if n.List != nil {
					findActions(n.List.Nodes)
				}
				if n.ElseList != nil {
					findActions(n.ElseList.Nodes)
				}
			}
		}
	}
	findActions(tree.Root.Nodes)

	blocks, err := Extract(tree, actionTypes)
	if err != nil {
		t.Fatal(err)
	}

	if len(blocks) != 1 {
		t.Fatalf("expected 1 script block, got %d", len(blocks))
	}

	block := blocks[0]
	if !strings.Contains(block.TypeScript, `console.log("debug:"`) {
		t.Errorf("expected debug log in output, got:\n%s", block.TypeScript)
	}
	if !strings.Contains(block.TypeScript, "(undefined! as string)") {
		t.Errorf("expected typed placeholder, got:\n%s", block.TypeScript)
	}
	// Scalar types should not produce warnings.
	if len(block.Warnings) > 0 {
		t.Errorf("unexpected warnings for scalar types: %v", block.Warnings)
	}
	t.Logf("Generated TypeScript:\n%s", block.TypeScript)
}

func TestExtractJSONParse(t *testing.T) {
	const templateText = `<script>
  const items = JSON.parse({{.Items | json}});
</script>`

	// parse.Parse doesn't know about "json" as a function, so we need to
	// register it to avoid a parse error.
	funcs := map[string]any{"json": func() {}}
	trees, err := parse.Parse("test.gohtml", templateText, "", "", funcs)
	if err != nil {
		t.Fatal(err)
	}
	tree := trees["test.gohtml"]

	itemsType := types.NewSlice(types.NewStruct(
		[]*types.Var{
			types.NewField(0, nil, "Name", types.Typ[types.String], false),
			types.NewField(0, nil, "Price", types.Typ[types.Float64], false),
		},
		[]string{"", ""},
	))

	actionTypes := make(map[*parse.ActionNode]ActionTypes)
	for _, node := range tree.Root.Nodes {
		if a, ok := node.(*parse.ActionNode); ok {
			pipeStr := a.String()
			if strings.Contains(pipeStr, ".Items") {
				// The pipe is .Items | json — input is []Item, resolved is string.
				actionTypes[a] = ActionTypes{
					InputType:    itemsType,
					ResolvedType: types.Typ[types.String],
				}
			}
		}
	}

	blocks, err := Extract(tree, actionTypes)
	if err != nil {
		t.Fatal(err)
	}

	if len(blocks) != 1 {
		t.Fatalf("expected 1 script block, got %d", len(blocks))
	}

	block := blocks[0]
	// Should use the input type ([]Item), not the resolved type (string).
	if !strings.Contains(block.TypeScript, `(undefined! as Array<{ Name: string; Price: number }>)`) {
		t.Errorf("expected input type placeholder for JSON.parse, got:\n%s", block.TypeScript)
	}
	// Should NOT contain JSON.parse wrapper (it's been replaced).
	if strings.Contains(block.TypeScript, "JSON.parse") {
		t.Errorf("JSON.parse should be replaced in output:\n%s", block.TypeScript)
	}
	// No warnings expected — JSON.parse wrapping is correct usage.
	if len(block.Warnings) > 0 {
		t.Errorf("unexpected warnings with JSON.parse: %v", block.Warnings)
	}
	t.Logf("Generated TypeScript:\n%s", block.TypeScript)
}

func TestExtractPipedToJSON(t *testing.T) {
	// {{.Items | json}} — resolved type is string, no warning expected.
	const templateText = `<script>
  const items = {{.Items | json}};
</script>`

	funcs := map[string]any{"json": func() {}}
	trees, err := parse.Parse("test.gohtml", templateText, "", "", funcs)
	if err != nil {
		t.Fatal(err)
	}
	tree := trees["test.gohtml"]

	itemsType := types.NewSlice(types.NewStruct(
		[]*types.Var{
			types.NewField(0, nil, "Name", types.Typ[types.String], false),
		},
		[]string{""},
	))

	actionTypes := make(map[*parse.ActionNode]ActionTypes)
	for _, node := range tree.Root.Nodes {
		if a, ok := node.(*parse.ActionNode); ok {
			if strings.Contains(a.String(), ".Items") {
				actionTypes[a] = ActionTypes{
					InputType:    itemsType,
					ResolvedType: types.Typ[types.String], // json func returns string
				}
			}
		}
	}

	blocks, err := Extract(tree, actionTypes)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blocks))
	}

	block := blocks[0]
	// Resolved type is string, so placeholder should be string.
	if !strings.Contains(block.TypeScript, "(undefined! as string)") {
		t.Errorf("expected string placeholder, got:\n%s", block.TypeScript)
	}
	// No warning — piped through json, resolved type is scalar.
	if len(block.Warnings) > 0 {
		t.Errorf("unexpected warnings for json-piped value: %v", block.Warnings)
	}
	t.Logf("Generated TypeScript:\n%s", block.TypeScript)
}

func TestExtractNoScript(t *testing.T) {
	const templateText = `<h1>{{.Title}}</h1><p>{{.Body}}</p>`

	trees, err := parse.Parse("test.gohtml", templateText, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}

	blocks, err := Extract(trees["test.gohtml"], nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 0 {
		t.Fatalf("expected 0 script blocks, got %d", len(blocks))
	}
}
