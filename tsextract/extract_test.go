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

	result, err := Extract(tree, actionTypes)
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || len(result.ScriptBlocks) != 1 {
		t.Fatalf("expected 1 script block, got %v", result)
	}

	block := result.ScriptBlocks[0]
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

	result, err := Extract(tree, actionTypes)
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || len(result.ScriptBlocks) != 1 {
		t.Fatalf("expected 1 script block, got %v", result)
	}

	block := result.ScriptBlocks[0]
	if !strings.Contains(block.TypeScript, `console.log("debug:"`) {
		t.Errorf("expected debug log in output, got:\n%s", block.TypeScript)
	}
	if !strings.Contains(block.TypeScript, "(undefined! as string)") {
		t.Errorf("expected typed placeholder, got:\n%s", block.TypeScript)
	}
	if len(block.Warnings) > 0 {
		t.Errorf("unexpected warnings for scalar types: %v", block.Warnings)
	}
	t.Logf("Generated TypeScript:\n%s", block.TypeScript)
}

func TestExtractJSONParse(t *testing.T) {
	const templateText = `<script>
  const items = JSON.parse({{.Items | json}});
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
			types.NewField(0, nil, "Price", types.Typ[types.Float64], false),
		},
		[]string{"", ""},
	))

	actionTypes := make(map[*parse.ActionNode]ActionTypes)
	for _, node := range tree.Root.Nodes {
		if a, ok := node.(*parse.ActionNode); ok {
			if strings.Contains(a.String(), ".Items") {
				actionTypes[a] = ActionTypes{
					InputType:    itemsType,
					ResolvedType: types.Typ[types.String],
				}
			}
		}
	}

	result, err := Extract(tree, actionTypes)
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || len(result.ScriptBlocks) != 1 {
		t.Fatalf("expected 1 script block, got %v", result)
	}

	block := result.ScriptBlocks[0]
	if !strings.Contains(block.TypeScript, `(undefined! as Array<{ Name: string; Price: number }>)`) {
		t.Errorf("expected input type placeholder for JSON.parse, got:\n%s", block.TypeScript)
	}
	if strings.Contains(block.TypeScript, "JSON.parse") {
		t.Errorf("JSON.parse should be replaced in output:\n%s", block.TypeScript)
	}
	if len(block.Warnings) > 0 {
		t.Errorf("unexpected warnings with JSON.parse: %v", block.Warnings)
	}
	t.Logf("Generated TypeScript:\n%s", block.TypeScript)
}

func TestExtractPipedToJSON(t *testing.T) {
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
					ResolvedType: types.Typ[types.String],
				}
			}
		}
	}

	result, err := Extract(tree, actionTypes)
	if err != nil {
		t.Fatal(err)
	}
	if result == nil || len(result.ScriptBlocks) != 1 {
		t.Fatalf("expected 1 block, got %v", result)
	}

	block := result.ScriptBlocks[0]
	if !strings.Contains(block.TypeScript, "(undefined! as string)") {
		t.Errorf("expected string placeholder, got:\n%s", block.TypeScript)
	}
	if len(block.Warnings) > 0 {
		t.Errorf("unexpected warnings for json-piped value: %v", block.Warnings)
	}
	t.Logf("Generated TypeScript:\n%s", block.TypeScript)
}

func TestExtractJSONBlock(t *testing.T) {
	const templateText = `<script type="application/json" id="page-data">
{{.PageData | json}}
</script>
<script>
  const data = JSON.parse(document.getElementById("page-data").textContent);
  console.log(data.Name);
</script>`

	funcs := map[string]any{"json": func() {}}
	trees, err := parse.Parse("test.gohtml", templateText, "", "", funcs)
	if err != nil {
		t.Fatal(err)
	}
	tree := trees["test.gohtml"]

	pageDataType := types.NewStruct(
		[]*types.Var{
			types.NewField(0, nil, "Name", types.Typ[types.String], false),
			types.NewField(0, nil, "Count", types.Typ[types.Int], false),
		},
		[]string{"", ""},
	)

	actionTypes := make(map[*parse.ActionNode]ActionTypes)
	var findActions func([]parse.Node)
	findActions = func(nodes []parse.Node) {
		for _, node := range nodes {
			if a, ok := node.(*parse.ActionNode); ok {
				if strings.Contains(a.String(), ".PageData") {
					actionTypes[a] = ActionTypes{
						InputType:    pageDataType,
						ResolvedType: types.Typ[types.String],
					}
				}
			}
		}
	}
	findActions(tree.Root.Nodes)

	result, err := Extract(tree, actionTypes)
	if err != nil {
		t.Fatal(err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}

	// Should find the JSON block.
	if len(result.JSONBlocks) != 1 {
		t.Fatalf("expected 1 JSON block, got %d", len(result.JSONBlocks))
	}
	jb := result.JSONBlocks[0]
	if jb.ID != "page-data" {
		t.Errorf("expected id 'page-data', got %q", jb.ID)
	}
	if !strings.Contains(jb.TSTyp, "Name: string") {
		t.Errorf("expected type with Name field, got %q", jb.TSTyp)
	}

	// Should also find the JS script block.
	if len(result.ScriptBlocks) != 1 {
		t.Fatalf("expected 1 script block, got %d", len(result.ScriptBlocks))
	}

	// Format the full .ts file and check it has the branded types.
	output := FormatTSFile("test.gohtml", "test.gohtml", result)
	if !strings.Contains(output, "JSONString") {
		t.Errorf("expected JSONString brand in output, got:\n%s", output)
	}
	if !strings.Contains(output, `getElementById(id: "page-data"):`) {
		t.Errorf("expected getElementById overload, got:\n%s", output)
	}
	if !strings.Contains(output, "Name: string") {
		t.Errorf("expected typed textContent, got:\n%s", output)
	}
	t.Logf("Generated .ts file:\n%s", output)
}

func TestExtractNoScript(t *testing.T) {
	const templateText = `<h1>{{.Title}}</h1><p>{{.Body}}</p>`

	trees, err := parse.Parse("test.gohtml", templateText, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}

	result, err := Extract(trees["test.gohtml"], nil)
	if err != nil {
		t.Fatal(err)
	}
	if result != nil {
		t.Fatalf("expected nil result, got %v", result)
	}
}
