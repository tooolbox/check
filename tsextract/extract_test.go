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
	// Non-scalar type injected without JSON serialisation should be typed as 'unknown'.
	if !strings.Contains(block.TypeScript, `(undefined! as unknown)`) {
		t.Errorf("expected non-scalar Items to be typed as 'unknown', got:\n%s", block.TypeScript)
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

func TestExtractDataAttributes(t *testing.T) {
	const templateText = `<div id="app" data-config='{{.Config | json}}' data-name="{{.Name}}"></div>`

	funcs := map[string]any{"json": func() {}}
	trees, err := parse.Parse("test.gohtml", templateText, "", "", funcs)
	if err != nil {
		t.Fatal(err)
	}
	tree := trees["test.gohtml"]

	configType := types.NewStruct(
		[]*types.Var{
			types.NewField(0, nil, "Theme", types.Typ[types.String], false),
			types.NewField(0, nil, "Debug", types.Typ[types.Bool], false),
		},
		[]string{"", ""},
	)

	actionTypes := make(map[*parse.ActionNode]ActionTypes)
	var findActions func([]parse.Node)
	findActions = func(nodes []parse.Node) {
		for _, node := range nodes {
			if a, ok := node.(*parse.ActionNode); ok {
				pipeStr := a.String()
				switch {
				case strings.Contains(pipeStr, ".Config"):
					actionTypes[a] = ActionTypes{
						InputType:    configType,
						ResolvedType: types.Typ[types.String],
					}
				case strings.Contains(pipeStr, ".Name"):
					actionTypes[a] = scalarAction(types.Typ[types.String])
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
	if len(result.DataAttrBlocks) != 1 {
		t.Fatalf("expected 1 data attr block, got %d", len(result.DataAttrBlocks))
	}

	da := result.DataAttrBlocks[0]
	if da.ElementID != "app" {
		t.Errorf("expected element id 'app', got %q", da.ElementID)
	}
	if len(da.Attrs) < 1 {
		t.Fatalf("expected at least 1 data attr, got %d", len(da.Attrs))
	}

	// Check that we got both attrs (order may vary).
	foundConfig, foundName := false, false
	for _, a := range da.Attrs {
		switch a.Name {
		case "config":
			foundConfig = true
			if !a.IsJSON {
				t.Error("expected config attr to be JSON-piped")
			}
			if !strings.Contains(a.TSTyp, "Theme: string") {
				t.Errorf("expected config type with Theme, got %q", a.TSTyp)
			}
		case "name":
			foundName = true
			if a.IsJSON {
				t.Error("expected name attr to NOT be JSON-piped")
			}
			if a.TSTyp != "string" {
				t.Errorf("expected name type 'string', got %q", a.TSTyp)
			}
		}
	}
	if !foundConfig {
		t.Error("missing data-config attr")
	}
	if !foundName {
		t.Error("missing data-name attr")
	}

	// Check FormatTSFile output.
	output := FormatTSFile("test.gohtml", "test.gohtml", result)
	if !strings.Contains(output, `getElementById(id: "app")`) {
		t.Errorf("expected getElementById overload for 'app', got:\n%s", output)
	}
	if !strings.Contains(output, "dataset:") {
		t.Errorf("expected dataset in output, got:\n%s", output)
	}
	if !strings.Contains(output, "JSONString") {
		t.Errorf("expected JSONString brand in output, got:\n%s", output)
	}
	t.Logf("Generated .ts file:\n%s", output)
}

func TestExtractMetaTag(t *testing.T) {
	const templateText = `<meta name="csrf-token" content="{{.CSRFToken}}">
<meta name="page-config" content='{{.Config | json}}'>`

	funcs := map[string]any{"json": func() {}}
	trees, err := parse.Parse("test.gohtml", templateText, "", "", funcs)
	if err != nil {
		t.Fatal(err)
	}
	tree := trees["test.gohtml"]

	configType := types.NewStruct(
		[]*types.Var{
			types.NewField(0, nil, "Title", types.Typ[types.String], false),
		},
		[]string{""},
	)

	actionTypes := make(map[*parse.ActionNode]ActionTypes)
	for _, node := range tree.Root.Nodes {
		if a, ok := node.(*parse.ActionNode); ok {
			pipeStr := a.String()
			switch {
			case strings.Contains(pipeStr, ".CSRFToken"):
				actionTypes[a] = scalarAction(types.Typ[types.String])
			case strings.Contains(pipeStr, ".Config"):
				actionTypes[a] = ActionTypes{
					InputType:    configType,
					ResolvedType: types.Typ[types.String],
				}
			}
		}
	}

	result, err := Extract(tree, actionTypes)
	if err != nil {
		t.Fatal(err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if len(result.MetaBlocks) != 2 {
		t.Fatalf("expected 2 meta blocks, got %d", len(result.MetaBlocks))
	}

	// Check scalar meta.
	csrf := result.MetaBlocks[0]
	if csrf.Name != "csrf-token" {
		t.Errorf("expected name 'csrf-token', got %q", csrf.Name)
	}
	if csrf.IsJSON {
		t.Error("expected csrf-token to NOT be JSON-piped")
	}
	if csrf.TSTyp != "string" {
		t.Errorf("expected type 'string', got %q", csrf.TSTyp)
	}

	// Check JSON meta.
	config := result.MetaBlocks[1]
	if config.Name != "page-config" {
		t.Errorf("expected name 'page-config', got %q", config.Name)
	}
	if !config.IsJSON {
		t.Error("expected page-config to be JSON-piped")
	}

	// Check FormatTSFile output.
	output := FormatTSFile("test.gohtml", "test.gohtml", result)
	if !strings.Contains(output, `querySelector(selector: 'meta[name="csrf-token"]')`) {
		t.Errorf("expected querySelector overload for csrf-token, got:\n%s", output)
	}
	if !strings.Contains(output, `querySelector(selector: 'meta[name="page-config"]')`) {
		t.Errorf("expected querySelector overload for page-config, got:\n%s", output)
	}
	t.Logf("Generated .ts file:\n%s", output)
}

func TestExtractHiddenInput(t *testing.T) {
	const templateText = `<input type="hidden" id="user-data" value='{{.User | json}}'>`

	funcs := map[string]any{"json": func() {}}
	trees, err := parse.Parse("test.gohtml", templateText, "", "", funcs)
	if err != nil {
		t.Fatal(err)
	}
	tree := trees["test.gohtml"]

	userType := types.NewStruct(
		[]*types.Var{
			types.NewField(0, nil, "Name", types.Typ[types.String], false),
			types.NewField(0, nil, "Email", types.Typ[types.String], false),
		},
		[]string{"", ""},
	)

	actionTypes := make(map[*parse.ActionNode]ActionTypes)
	for _, node := range tree.Root.Nodes {
		if a, ok := node.(*parse.ActionNode); ok {
			if strings.Contains(a.String(), ".User") {
				actionTypes[a] = ActionTypes{
					InputType:    userType,
					ResolvedType: types.Typ[types.String],
				}
			}
		}
	}

	result, err := Extract(tree, actionTypes)
	if err != nil {
		t.Fatal(err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	if len(result.HiddenInputBlocks) != 1 {
		t.Fatalf("expected 1 hidden input block, got %d", len(result.HiddenInputBlocks))
	}

	hi := result.HiddenInputBlocks[0]
	if hi.ElementID != "user-data" {
		t.Errorf("expected element id 'user-data', got %q", hi.ElementID)
	}
	if !hi.IsJSON {
		t.Error("expected hidden input to be JSON-piped")
	}
	if !strings.Contains(hi.TSTyp, "Name: string") {
		t.Errorf("expected type with Name field, got %q", hi.TSTyp)
	}

	// Check FormatTSFile output.
	output := FormatTSFile("test.gohtml", "test.gohtml", result)
	if !strings.Contains(output, `getElementById(id: "user-data")`) {
		t.Errorf("expected getElementById overload, got:\n%s", output)
	}
	if !strings.Contains(output, "HTMLInputElement") {
		t.Errorf("expected HTMLInputElement in output, got:\n%s", output)
	}
	if !strings.Contains(output, "JSONString") {
		t.Errorf("expected JSONString brand, got:\n%s", output)
	}
	t.Logf("Generated .ts file:\n%s", output)
}

func TestSourceMapping(t *testing.T) {
	// Template with a script block starting at a known line.
	// Line 1: <h1>{{.Title}}</h1>
	// Line 2: <script>
	// Line 3:   const x = {{.X}};
	// Line 4:   const y = {{.Y}};
	// Line 5:   console.log(x + y);
	// Line 6: </script>
	const templateText = "<h1>{{.Title}}</h1>\n<script>\n  const x = {{.X}};\n  const y = {{.Y}};\n  console.log(x + y);\n</script>"

	trees, err := parse.Parse("test.gohtml", templateText, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	tree := trees["test.gohtml"]

	actionTypes := make(map[*parse.ActionNode]ActionTypes)
	var findActions func([]parse.Node)
	findActions = func(nodes []parse.Node) {
		for _, node := range nodes {
			if a, ok := node.(*parse.ActionNode); ok {
				pipeStr := a.String()
				switch {
				case strings.Contains(pipeStr, ".Title"):
					actionTypes[a] = scalarAction(types.Typ[types.String])
				case strings.Contains(pipeStr, ".X"):
					actionTypes[a] = scalarAction(types.Typ[types.Int])
				case strings.Contains(pipeStr, ".Y"):
					actionTypes[a] = scalarAction(types.Typ[types.Int])
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
	t.Logf("TypeScript:\n%s", block.TypeScript)
	t.Logf("LineMap: %v", block.LineMap)

	// The script content (after TrimSpace) should be:
	// Line 1: "const x = (undefined! as number);"   -> template line 3
	// Line 2: "const y = (undefined! as number);"   -> template line 4
	// Line 3: "console.log(x + y);"                 -> template line 5
	if len(block.LineMap) < 3 {
		t.Fatalf("expected at least 3 entries in LineMap, got %d", len(block.LineMap))
	}

	// Verify the mapping.
	if block.LineMap[0] != 3 {
		t.Errorf("TS line 1 should map to template line 3, got %d", block.LineMap[0])
	}
	if block.LineMap[1] != 4 {
		t.Errorf("TS line 2 should map to template line 4, got %d", block.LineMap[1])
	}
	if block.LineMap[2] != 5 {
		t.Errorf("TS line 3 should map to template line 5, got %d", block.LineMap[2])
	}

	// Test MapLocation helper.
	// Template line 3 is "  const x = {{.X}};", TS line 1 is "const x = (undefined! as number);".
	// TS col 15 is inside the replacement, so it clamps to the start of {{.X}} at template col 13.
	loc := block.MapLocation(1, 15)
	if loc.Line != 3 {
		t.Errorf("MapLocation(1,15) line should be 3, got %d", loc.Line)
	}
	if loc.Col != 13 {
		t.Errorf("MapLocation(1,15) col should be 13 (start of {{.X}}), got %d", loc.Col)
	}

	// Out of range should return StartLine.
	loc = block.MapLocation(100, 1)
	if loc.Line != block.StartLine {
		t.Errorf("MapLocation(100,1) should fall back to StartLine %d, got %d", block.StartLine, loc.Line)
	}
}

func TestSourceMappingWithControlFlow(t *testing.T) {
	// Control flow flattening can shift lines. Verify the mapping still works.
	// Line 1: <script>
	// Line 2: {{if .Debug}}
	// Line 3:   console.log("debug");
	// Line 4: {{end}}
	// Line 5:   const title = {{.Title}};
	// Line 6: </script>
	const templateText = "<script>\n{{if .Debug}}\n  console.log(\"debug\");\n{{end}}\n  const title = {{.Title}};\n</script>"

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
				if strings.Contains(n.String(), ".Title") {
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
	t.Logf("TypeScript:\n%s", block.TypeScript)
	t.Logf("LineMap: %v", block.LineMap)

	// The line containing "console.log" should map to template line 3.
	tsLines := strings.Split(block.TypeScript, "\n")
	for i, line := range tsLines {
		if strings.Contains(line, "console.log") {
			if i < len(block.LineMap) && block.LineMap[i] != 3 {
				t.Errorf("console.log line (TS line %d) should map to template line 3, got %d", i+1, block.LineMap[i])
			}
			break
		}
	}

	// The line containing "const title" should map to template line 5.
	for i, line := range tsLines {
		if strings.Contains(line, "const title") {
			if i < len(block.LineMap) && block.LineMap[i] != 5 {
				t.Errorf("const title line (TS line %d) should map to template line 5, got %d", i+1, block.LineMap[i])
			}
			break
		}
	}
}

func TestColumnMapping(t *testing.T) {
	// Template where we can verify column mapping precisely.
	// Line 1: <script>
	// Line 2:   var x = {{.Title}};
	// Line 3: </script>
	//
	// In the template, "{{.Title}}" starts at column 11 on line 2 and is 10 chars.
	// In the TS output, it becomes "(undefined! as string)" which is 22 chars.
	// So:
	//   - TS col 1-10 (before replacement) → template col 1-10 (unchanged)
	//   - TS col 11 (start of replacement) → template col 11 (start of {{.Title}})
	//   - TS col 11-32 (inside replacement) → template col 11 (clamped to action start)
	//   - TS col 33 (the ";") → template col 21 (after {{.Title}} which ends at col 20)
	const templateText = "<script>\n  var x = {{.Title}};\n</script>"

	trees, err := parse.Parse("test.gohtml", templateText, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	tree := trees["test.gohtml"]

	actionTypes := make(map[*parse.ActionNode]ActionTypes)
	var findActions func([]parse.Node)
	findActions = func(nodes []parse.Node) {
		for _, node := range nodes {
			if a, ok := node.(*parse.ActionNode); ok {
				if strings.Contains(a.String(), ".Title") {
					actionTypes[a] = scalarAction(types.Typ[types.String])
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
	t.Logf("TypeScript:\n%s", block.TypeScript)
	t.Logf("LineMap: %v", block.LineMap)
	t.Logf("ColMap: %v", block.ColMap)

	// The TS output should be: "var x = (undefined! as string);"
	if !strings.Contains(block.TypeScript, "var x = (undefined! as string);") {
		t.Fatalf("unexpected TS output: %s", block.TypeScript)
	}

	// Test column mapping on the line with the replacement.
	// Find which TS line has "var x".
	tsLines := strings.Split(block.TypeScript, "\n")
	varLine := -1
	for i, line := range tsLines {
		if strings.Contains(line, "var x") {
			varLine = i + 1 // 1-based
			break
		}
	}
	if varLine < 0 {
		t.Fatal("could not find 'var x' line")
	}

	// Template line 2 is "  var x = {{.Title}};".
	// TS output is "var x = (undefined! as string);" (2 leading spaces trimmed).
	// So TS col 1 ("v") maps to template col 3.
	loc := block.MapLocation(varLine, 1)
	if loc.Col != 3 {
		t.Errorf("col 1 (before replacement): expected template col 3, got %d", loc.Col)
	}

	// TS col 9 (start of "(undefined! as string)") — maps to template col 11 (start of {{.Title}}).
	loc = block.MapLocation(varLine, 9)
	t.Logf("MapLocation(%d, 9) = %+v", varLine, loc)
	if loc.Col != 11 {
		t.Errorf("col 9 (start of replacement): expected template col 11, got %d", loc.Col)
	}

	// Column inside the replacement — should clamp to the original action start.
	loc15 := block.MapLocation(varLine, 15)
	if loc15.Col != 11 {
		t.Errorf("col 15 (inside replacement) should clamp to template col 11, got %d", loc15.Col)
	}

	// Column after the replacement (the ";").
	// TS: "(undefined! as string)" is 22 chars starting at col 9, so ";" is at TS col 31.
	// Template: "{{.Title}}" is 10 chars starting at col 11, so ";" is at template col 21.
	semiCol := 9 + len("(undefined! as string)")
	locSemi := block.MapLocation(varLine, semiCol)
	t.Logf("semicolon: TS col %d -> template col %d (expected 21)", semiCol, locSemi.Col)
	if locSemi.Col != 21 {
		t.Errorf("col after replacement: expected template col 21, got %d", locSemi.Col)
	}
}

func TestColumnMappingMultipleReplacements(t *testing.T) {
	// Two replacements on the same line.
	// Line 1: <script>
	// Line 2:   var s = {{.A}} + {{.B}};
	// Line 3: </script>
	const templateText = "<script>\n  var s = {{.A}} + {{.B}};\n</script>"

	trees, err := parse.Parse("test.gohtml", templateText, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	tree := trees["test.gohtml"]

	actionTypes := make(map[*parse.ActionNode]ActionTypes)
	var findActions func([]parse.Node)
	findActions = func(nodes []parse.Node) {
		for _, node := range nodes {
			if a, ok := node.(*parse.ActionNode); ok {
				pipeStr := a.String()
				if strings.Contains(pipeStr, ".A") || strings.Contains(pipeStr, ".B") {
					actionTypes[a] = scalarAction(types.Typ[types.Int])
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
	t.Logf("TypeScript:\n%s", block.TypeScript)

	// Find the "var s" line.
	tsLines := strings.Split(block.TypeScript, "\n")
	varLine := -1
	for i, line := range tsLines {
		if strings.Contains(line, "var s") {
			varLine = i + 1
			break
		}
	}
	if varLine < 0 {
		t.Fatal("could not find 'var s' line")
	}

	for i, spans := range block.ColMap {
		t.Logf("ColMap[%d]: %v", i, spans)
	}

	// The ";" after both replacements should map back to the correct template column.
	// Template: "  var s = {{.A}} + {{.B}};"
	//            123456789012345678901234567
	// {{.A}} starts at col 11, length 6
	// " + " at col 17-19
	// {{.B}} starts at col 20, length 6
	// ";" at col 26
	//
	// TS: "  var s = (undefined! as number) + (undefined! as number);"
	// (undefined! as number) is 22 chars.
	// Starts: col 11, length 22. Then " + " then col 36, length 22. ";" at col 58.
	semiTSCol := strings.Index(block.TypeScript, ";")
	if semiTSCol < 0 {
		t.Fatal("no semicolon found")
	}
	// Convert to 1-based column within the line.
	lineStart := 0
	for i, line := range tsLines {
		if i+1 == varLine {
			break
		}
		lineStart += len(line) + 1
	}
	semiTSColInLine := semiTSCol - lineStart + 1

	locSemi := block.MapLocation(varLine, semiTSColInLine)
	t.Logf("semicolon: TS col %d -> template col %d", semiTSColInLine, locSemi.Col)

	// The semicolon should map to template col 26.
	if locSemi.Col != 26 {
		t.Errorf("expected template col 26 for semicolon, got %d", locSemi.Col)
	}
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
