package tsextract

import (
	"bytes"
	"fmt"
	"go/types"
	"strings"
	"text/template/parse"

	"golang.org/x/net/html"
)

// ActionTypes holds the resolved types for a template action node.
type ActionTypes struct {
	// InputType is the type of the first expression in the pipe (before
	// any functions are applied), e.g. []Item for {{.Items | json}}.
	InputType types.Type
	// ResolvedType is the final pipe result type, e.g. string for {{.Items | json}}.
	ResolvedType types.Type
}

// JSONBlock represents a <script type="application/json" id="..."> block
// whose content type has been resolved from the template action inside it.
type JSONBlock struct {
	ID    string     // the id attribute
	TSTyp string     // the TypeScript type string
	Type  types.Type // the Go input type
}

// ScriptBlock represents one <script>...</script> region extracted from a template.
type ScriptBlock struct {
	TemplateName string
	TemplateFile string // absolute path to the .gohtml/.tmpl file
	StartLine    int    // line of <script> in template source
	TypeScript   string // the generated TS content
	// Warnings collects non-fatal issues found during extraction.
	Warnings []string
}

// ExtractResult holds both the generated script blocks and any JSON data blocks
// found in the template.
type ExtractResult struct {
	ScriptBlocks []ScriptBlock
	JSONBlocks   []JSONBlock
}

// Extract finds <script> blocks in the template source, replaces template
// actions with typed TypeScript placeholders, and returns the generated
// TypeScript content for each script block.
//
// It also detects <script type="application/json" id="..."> blocks and
// captures their Go types so that typed getElementById overloads can be
// generated in the output.
//
// Control flow nodes (if/range/with) are flattened: all branches are emitted
// and the control expressions are stripped.
func Extract(tree *parse.Tree, actions map[*parse.ActionNode]ActionTypes) (*ExtractResult, error) {
	// Flatten the tree into a sequence of text and action nodes,
	// inlining control flow.
	var flat []parse.Node
	flattenNodes(tree.Root.Nodes, &flat)

	// Build a concatenated HTML string from text nodes, inserting unique
	// placeholders for action nodes. Track which placeholder maps to which action.
	type placeholder struct {
		index  int // index in flat list
		action *parse.ActionNode
	}
	var buf bytes.Buffer
	var placeholders []placeholder
	phIndex := 0

	for i, node := range flat {
		switch n := node.(type) {
		case *parse.TextNode:
			buf.Write(n.Text)
		case *parse.ActionNode:
			// Variable assignments produce no output in HTML.
			if isAssignment(n) {
				continue
			}
			marker := fmt.Sprintf("__TSEXTRACT_%d__", phIndex)
			buf.WriteString(marker)
			placeholders = append(placeholders, placeholder{index: i, action: n})
			phIndex++
		}
	}

	// Tokenize the concatenated HTML to find <script> boundaries and
	// <script type="application/json"> blocks.
	concatenated := buf.String()
	parsed := parseScriptTags(concatenated)

	if len(parsed.jsRanges) == 0 && len(parsed.jsonBlocks) == 0 {
		return nil, nil
	}

	result := &ExtractResult{}

	// Resolve JSON blocks: find placeholders inside them and map to Go types.
	for _, jb := range parsed.jsonBlocks {
		content := concatenated[jb.start:jb.end]
		for j, ph := range placeholders {
			marker := fmt.Sprintf("__TSEXTRACT_%d__", j)
			if !strings.Contains(content, marker) {
				continue
			}
			at, ok := actions[ph.action]
			if !ok {
				continue
			}
			// Use the input type (before pipe) since the pipe typically
			// goes through a JSON marshaller.
			typ := at.InputType
			if typ == nil {
				typ = at.ResolvedType
			}
			result.JSONBlocks = append(result.JSONBlocks, JSONBlock{
				ID:    jb.id,
				TSTyp: GoTypeToTS(typ),
				Type:  typ,
			})
		}
	}

	// Process JS script blocks.
	for _, sr := range parsed.jsRanges {
		scriptContent := concatenated[sr.start:sr.end]
		var warnings []string

		// Replace each placeholder in this script range with a typed TS expression.
		for j, ph := range placeholders {
			marker := fmt.Sprintf("__TSEXTRACT_%d__", j)
			if !strings.Contains(scriptContent, marker) {
				continue
			}
			at, ok := actions[ph.action]

			var replacement string
			if !ok || at.ResolvedType == nil {
				replacement = "(undefined! as unknown)"
			} else if jsonParseWrapped(scriptContent, marker) {
				// JSON.parse({{.X | json}}) — use the input type (pre-pipe)
				// and replace the entire JSON.parse(...) span.
				tsType := GoTypeToTS(at.InputType)
				scriptContent = strings.ReplaceAll(scriptContent,
					"JSON.parse("+marker+")",
					fmt.Sprintf("(undefined! as %s)", tsType))
				continue
			} else {
				if isNonScalar(at.ResolvedType) {
					warnings = append(warnings, fmt.Sprintf(
						"non-scalar type %s injected into <script> without JSON serialisation; output will be Go's %%v format, not valid JS (W008)",
						at.ResolvedType))
				}
				tsType := GoTypeToTS(at.ResolvedType)
				replacement = fmt.Sprintf("(undefined! as %s)", tsType)
			}
			scriptContent = strings.ReplaceAll(scriptContent, marker, replacement)
		}
		result.ScriptBlocks = append(result.ScriptBlocks, ScriptBlock{
			TemplateName: tree.Name,
			TemplateFile: tree.ParseName,
			StartLine:    sr.line,
			TypeScript:   strings.TrimSpace(scriptContent),
			Warnings:     warnings,
		})
	}

	return result, nil
}

// FormatTSFile builds the complete .ts file content from an ExtractResult.
func FormatTSFile(templateBaseName, templateFullPath string, result *ExtractResult) string {
	var buf strings.Builder
	fmt.Fprintf(&buf, "// %s — auto-generated by check-templates -o ts-extract\n", templateBaseName)
	fmt.Fprintf(&buf, "// Source: %s\n\n", templateFullPath)

	// Emit branded JSON type infrastructure if there are JSON blocks.
	if len(result.JSONBlocks) > 0 {
		buf.WriteString("declare const __brand: unique symbol;\n")
		buf.WriteString("type JSONString<T> = string & { [__brand]: T };\n")
		buf.WriteString("interface JSON { parse<T>(text: JSONString<T>): T; parse(text: string): any; }\n")

		buf.WriteString("interface Document {\n")
		for _, jb := range result.JSONBlocks {
			fmt.Fprintf(&buf, "  getElementById(id: %q): (HTMLScriptElement & { textContent: JSONString<%s> }) | null;\n",
				jb.ID, jb.TSTyp)
		}
		buf.WriteString("}\n\n")
	}

	for i, block := range result.ScriptBlocks {
		if i > 0 {
			buf.WriteString("\n")
		}
		if len(result.ScriptBlocks) > 1 {
			fmt.Fprintf(&buf, "// <script> block %d (line %d)\n", i+1, block.StartLine)
		}
		buf.WriteString(block.TypeScript)
		buf.WriteString("\n")
	}

	return buf.String()
}

type scriptRange struct {
	start int // byte offset in concatenated string
	end   int // byte offset in concatenated string
	line  int // approximate line number
}

type jsonBlockRange struct {
	id    string // the id attribute value
	start int
	end   int
}

type parsedScripts struct {
	jsRanges   []scriptRange
	jsonBlocks []jsonBlockRange
}

// parseScriptTags uses the html tokenizer to locate both regular <script> blocks
// and <script type="application/json" id="..."> blocks.
func parseScriptTags(htmlStr string) parsedScripts {
	var result parsedScripts
	tokenizer := html.NewTokenizer(strings.NewReader(htmlStr))

	for {
		tt := tokenizer.Next()
		switch tt {
		case html.ErrorToken:
			return result

		case html.StartTagToken:
			tn, hasAttr := tokenizer.TagName()
			if string(tn) != "script" {
				continue
			}

			// Read attributes to check for type="application/json" and id="...".
			var scriptType, scriptID string
			if hasAttr {
				for {
					key, val, more := tokenizer.TagAttr()
					switch string(key) {
					case "type":
						scriptType = string(val)
					case "id":
						scriptID = string(val)
					}
					if !more {
						break
					}
				}
			}

			// Read the content between <script> and </script>.
			tt = tokenizer.Next()
			if tt != html.TextToken {
				continue
			}
			raw := tokenizer.Raw()
			text := string(raw)
			idx := strings.Index(htmlStr, text)
			if idx < 0 {
				continue
			}

			isJSON := strings.EqualFold(scriptType, "application/json")
			if isJSON && scriptID != "" {
				result.jsonBlocks = append(result.jsonBlocks, jsonBlockRange{
					id:    scriptID,
					start: idx,
					end:   idx + len(text),
				})
			} else if !isJSON {
				line := 1 + strings.Count(htmlStr[:idx], "\n")
				result.jsRanges = append(result.jsRanges, scriptRange{
					start: idx,
					end:   idx + len(text),
					line:  line,
				})
			}
		}
	}
}

// flattenNodes recursively flattens a node list, inlining control flow
// branches. All branches of if/with/range are emitted (Phase 1 semantics).
func flattenNodes(nodes []parse.Node, out *[]parse.Node) {
	for _, node := range nodes {
		switch n := node.(type) {
		case *parse.TextNode:
			*out = append(*out, n)
		case *parse.ActionNode:
			*out = append(*out, n)
		case *parse.IfNode:
			flattenBranch(&n.BranchNode, out)
		case *parse.WithNode:
			flattenBranch(&n.BranchNode, out)
		case *parse.RangeNode:
			flattenBranch(&n.BranchNode, out)
		case *parse.ListNode:
			if n != nil {
				flattenNodes(n.Nodes, out)
			}
		case *parse.TemplateNode:
			// Sub-template calls inside script blocks — emit as an action placeholder.
			// Phase 1: treat as unknown type.
		case *parse.CommentNode:
			// skip
		}
	}
}

func flattenBranch(b *parse.BranchNode, out *[]parse.Node) {
	if b.List != nil {
		flattenNodes(b.List.Nodes, out)
	}
	if b.ElseList != nil {
		flattenNodes(b.ElseList.Nodes, out)
	}
}

// isAssignment returns true if the action is a variable assignment that
// produces no output (e.g. {{$x := .Field}}).
func isAssignment(n *parse.ActionNode) bool {
	if n.Pipe == nil {
		return false
	}
	return len(n.Pipe.Decl) > 0
}

// jsonParseWrapped reports whether the marker appears inside a JSON.parse(...)
// call in the script content.
func jsonParseWrapped(scriptContent, marker string) bool {
	return strings.Contains(scriptContent, "JSON.parse("+marker+")")
}

// isNonScalar reports whether a Go type would produce invalid JavaScript when
// injected via {{.X}} without JSON marshalling (i.e. it renders as Go's %v).
func isNonScalar(typ types.Type) bool {
	if typ == nil {
		return false
	}
	// Unwrap pointers.
	for {
		ptr, ok := typ.(*types.Pointer)
		if !ok {
			break
		}
		typ = ptr.Elem()
	}
	switch typ.Underlying().(type) {
	case *types.Basic:
		return false // string, int, float, bool are fine
	case *types.Interface:
		return false // could be anything, don't warn
	default:
		return true // struct, slice, map, array, etc.
	}
}
