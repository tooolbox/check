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

// DataAttr represents a single data-* attribute with a resolved type.
type DataAttr struct {
	Name   string     // attribute name without "data-" prefix, e.g. "config"
	TSTyp  string     // TypeScript type string
	IsJSON bool       // true if piped through json (use JSONString<T> brand)
	Type   types.Type // Go type
}

// DataAttrBlock represents an element with id and data-* attributes
// containing template actions.
type DataAttrBlock struct {
	ElementID string     // the id attribute value
	Attrs     []DataAttr // each data-* attribute with resolved type
}

// MetaBlock represents a <meta name="..." content="{{...}}"> tag.
type MetaBlock struct {
	Name   string     // the name attribute value
	TSTyp  string     // TypeScript type of content
	IsJSON bool       // whether content is json-piped
	Type   types.Type // Go type
}

// HiddenInputBlock represents an <input type="hidden" id="..." value="{{...}}"> tag.
type HiddenInputBlock struct {
	ElementID string     // the id attribute value
	TSTyp     string     // TypeScript type of value
	IsJSON    bool       // whether value is json-piped
	Type      types.Type // Go type
}

// SourceLoc represents a location in a source file.
type SourceLoc struct {
	Line int // 1-based line number
	Col  int // 1-based column (byte offset within line)
}

// colSpan records a single replacement on a line: the TS output column range
// [TSCol, TSCol+NewLen) maps back to the template column range
// [OrigCol, OrigCol+OrigLen).
type colSpan struct {
	TSCol   int // 1-based column in the TS output where the replacement starts
	OrigCol int // 1-based column in the template source where the original action starts
	OrigLen int // length of the original template action (e.g. {{.Title}})
	NewLen  int // length of the replacement (e.g. (undefined! as string))
}

// ScriptBlock represents one <script>...</script> region extracted from a template.
type ScriptBlock struct {
	TemplateName string
	TemplateFile string // absolute path to the .gohtml/.tmpl file
	StartLine    int    // line of <script> in template source
	TypeScript   string // the generated TS content
	// LineMap maps each line of the generated TypeScript (0-indexed) to the
	// corresponding line in the original template source (1-based).
	// LineMap[0] is the template line for TS output line 1.
	LineMap []int
	// ColMap holds per-line column adjustment spans. ColMap[i] contains
	// the replacement spans for TS output line i+1. Lines with no
	// replacements have nil entries.
	ColMap [][]colSpan
	// ColOffset is the number of leading characters trimmed from the first
	// line during TrimSpace. This shifts all TS columns relative to the
	// template source (e.g. if 2 spaces were trimmed, TS col 1 maps to
	// template col 3 before any replacement adjustments).
	ColOffset int
	// Warnings collects non-fatal issues found during extraction.
	Warnings []string
}

// MapLocation translates a TypeScript location (1-based line/col from tsc
// diagnostics) to the corresponding template source location.
func (sb *ScriptBlock) MapLocation(tsLine, tsCol int) SourceLoc {
	if tsLine < 1 || tsLine > len(sb.LineMap) {
		return SourceLoc{Line: sb.StartLine, Col: 1}
	}
	colOffset := 0
	if tsLine == 1 {
		colOffset = sb.ColOffset
	}
	origCol := tsCol
	if tsLine-1 < len(sb.ColMap) {
		origCol = adjustCol(tsCol, sb.ColMap[tsLine-1], colOffset)
	} else {
		origCol += colOffset
	}
	return SourceLoc{
		Line: sb.LineMap[tsLine-1],
		Col:  origCol,
	}
}

// adjustCol translates a 1-based column in the TS output back to the
// corresponding 1-based column in the template source, accounting for
// replacement spans on this line.
// adjustCol translates a 1-based column in the TS output back to the
// corresponding 1-based column in the template source, accounting for
// replacement spans on this line and a base column offset (from leading
// whitespace trim on the first line).
func adjustCol(tsCol int, spans []colSpan, colOffset int) int {
	if len(spans) == 0 {
		return tsCol + colOffset
	}
	// Walk through spans in order. Each span shifts subsequent columns.
	// cumShift tracks the cumulative difference between TS and original columns.
	cumShift := 0
	for _, s := range spans {
		// s.TSCol is in TS output coordinates.
		if tsCol < s.TSCol {
			// Before this replacement — apply accumulated shift and offset.
			return tsCol + colOffset - cumShift
		}
		if tsCol < s.TSCol+s.NewLen {
			// Inside the replacement — clamp to the start of the original action.
			// OrigCol is already in template coordinates, no offset needed.
			return s.OrigCol
		}
		// After this replacement — accumulate the shift.
		cumShift += s.NewLen - s.OrigLen
	}
	return tsCol + colOffset - cumShift
}

// ExtractResult holds the generated script blocks and any typed HTML elements
// found in the template.
type ExtractResult struct {
	ScriptBlocks      []ScriptBlock
	JSONBlocks        []JSONBlock
	DataAttrBlocks    []DataAttrBlock
	MetaBlocks        []MetaBlock
	HiddenInputBlocks []HiddenInputBlock
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
	//
	// sourcePos tracks the template source byte offset for each byte in the
	// concatenated buffer, enabling source mapping back to template lines.
	var buf bytes.Buffer
	var sourcePos []int // sourcePos[i] = template byte offset for concat byte i
	var placeholders []placeholder
	phIndex := 0

	templateSource := tree.Root.String()
	_ = templateSource // used only for line counting via sourcePos

	for i, node := range flat {
		switch n := node.(type) {
		case *parse.TextNode:
			startPos := int(n.Position())
			for j := range n.Text {
				sourcePos = append(sourcePos, startPos+j)
			}
			buf.Write(n.Text)
		case *parse.ActionNode:
			// Variable assignments produce no output in HTML.
			if isAssignment(n) {
				continue
			}
			marker := fmt.Sprintf("__TSEXTRACT_%d__", phIndex)
			// n.Position() points to the content inside {{ }}, not the opening
			// delimiter. Subtract 2 to get the position of "{{".
			actionPos := int(n.Position()) - 2
			if actionPos < 0 {
				actionPos = 0
			}
			for range marker {
				sourcePos = append(sourcePos, actionPos)
			}
			buf.WriteString(marker)
			placeholders = append(placeholders, placeholder{index: i, action: n})
			phIndex++
		}
	}

	// Tokenize the concatenated HTML to find <script> boundaries and
	// <script type="application/json"> blocks.
	concatenated := buf.String()
	parsed := parseHTMLElements(concatenated)

	if len(parsed.jsRanges) == 0 && len(parsed.jsonBlocks) == 0 &&
		len(parsed.dataAttrs) == 0 && len(parsed.metas) == 0 && len(parsed.hiddenInputs) == 0 {
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

	// Resolve data-* attribute blocks.
	for _, da := range parsed.dataAttrs {
		block := DataAttrBlock{ElementID: da.elementID}
		for attrName, attrVal := range da.attrs {
			ph, at, ok := resolveAttrPlaceholder(attrVal, placeholders, actions)
			if !ok || ph < 0 {
				continue
			}
			isJSON := isJSONPiped(at)
			typ := at.ResolvedType
			if isJSON && at.InputType != nil {
				typ = at.InputType
			}
			block.Attrs = append(block.Attrs, DataAttr{
				Name:   attrName,
				TSTyp:  GoTypeToTS(typ),
				IsJSON: isJSON,
				Type:   typ,
			})
		}
		if len(block.Attrs) > 0 {
			result.DataAttrBlocks = append(result.DataAttrBlocks, block)
		}
	}

	// Resolve <meta> blocks.
	for _, m := range parsed.metas {
		_, at, ok := resolveAttrPlaceholder(m.content, placeholders, actions)
		if !ok {
			continue
		}
		isJSON := isJSONPiped(at)
		typ := at.ResolvedType
		if isJSON && at.InputType != nil {
			typ = at.InputType
		}
		result.MetaBlocks = append(result.MetaBlocks, MetaBlock{
			Name:   m.name,
			TSTyp:  GoTypeToTS(typ),
			IsJSON: isJSON,
			Type:   typ,
		})
	}

	// Resolve <input type="hidden"> blocks.
	for _, hi := range parsed.hiddenInputs {
		_, at, ok := resolveAttrPlaceholder(hi.value, placeholders, actions)
		if !ok {
			continue
		}
		isJSON := isJSONPiped(at)
		typ := at.ResolvedType
		if isJSON && at.InputType != nil {
			typ = at.InputType
		}
		result.HiddenInputBlocks = append(result.HiddenInputBlocks, HiddenInputBlock{
			ElementID: hi.elementID,
			TSTyp:     GoTypeToTS(typ),
			IsJSON:    isJSON,
			Type:      typ,
		})
	}

	// Build a line table for the template source: lineStarts[i] is the byte
	// offset where line i+1 begins (0-indexed array, 1-based lines).
	templateText := tree.Root.String()
	lineStarts := buildLineStarts(templateText)

	// Process JS script blocks.
	for _, sr := range parsed.jsRanges {
		scriptContent := concatenated[sr.start:sr.end]
		var warnings []string

		// Build the line map before replacements (replacements don't add/remove
		// newlines, so the line structure is stable). For each line in the script
		// content region, find the corresponding template source line.
		lineMap := buildLineMap(concatenated, sourcePos, sr.start, sr.end, lineStarts)

		// Build a per-line column offset map for the pre-replacement content.
		// preLineOffsets[i] = byte offset within scriptContent where line i starts.
		preLineOffsets := []int{0}
		for i, c := range scriptContent {
			if c == '\n' {
				preLineOffsets = append(preLineOffsets, i+1)
			}
		}

		// Collect replacement records before mutating scriptContent.
		// Each record has: line index, marker text to replace, replacement text,
		// original template action length, and original column.
		type replRecord struct {
			lineIdx int    // 0-based line in script content
			find    string // text to replace (marker or JSON.parse(marker))
			repl    string // replacement text
			origCol int    // 1-based column in template source
			origLen int    // length of original template action
		}
		var replacements []replRecord

		for j, ph := range placeholders {
			marker := fmt.Sprintf("__TSEXTRACT_%d__", j)
			markerIdx := strings.Index(scriptContent, marker)
			if markerIdx < 0 {
				continue
			}
			at, ok := actions[ph.action]

			// Determine which line this marker is on.
			markerLine := 0
			for li := len(preLineOffsets) - 1; li >= 0; li-- {
				if preLineOffsets[li] <= markerIdx {
					markerLine = li
					break
				}
			}

			// Compute the original column in the template source.
			// The marker position in the concatenated string is sr.start + markerIdx.
			concatPos := sr.start + markerIdx
			origSrcOffset := 0
			if concatPos < len(sourcePos) {
				origSrcOffset = sourcePos[concatPos]
			}
			// Find which template line this is on and compute column.
			origLineIdx := byteOffsetToLine(origSrcOffset, lineStarts) - 1 // 0-based
			origCol := 1
			if origLineIdx >= 0 && origLineIdx < len(lineStarts) {
				origCol = origSrcOffset - lineStarts[origLineIdx] + 1
			}

			// Compute the original template action length.
			// The action node's String() gives the full {{...}} text.
			origActionLen := len(ph.action.String())

			var find, repl string
			if !ok || at.ResolvedType == nil {
				find = marker
				repl = "(undefined! as unknown)"
			} else if jsonParseWrapped(scriptContent, marker) {
				find = "JSON.parse(" + marker + ")"
				tsType := GoTypeToTS(at.InputType)
				repl = fmt.Sprintf("(undefined! as %s)", tsType)
				// For JSON.parse wrapping, the original span includes "JSON.parse(" + action + ")".
				origActionLen = len("JSON.parse(") + origActionLen + len(")")
				// Adjust origCol back to the start of "JSON.parse(".
				jpIdx := strings.Index(scriptContent, find)
				if jpIdx >= 0 {
					jpConcatPos := sr.start + jpIdx
					if jpConcatPos < len(sourcePos) {
						jpSrcOffset := sourcePos[jpConcatPos]
						jpOrigLineIdx := byteOffsetToLine(jpSrcOffset, lineStarts) - 1
						if jpOrigLineIdx >= 0 && jpOrigLineIdx < len(lineStarts) {
							origCol = jpSrcOffset - lineStarts[jpOrigLineIdx] + 1
						}
					}
				}
			} else if isNonScalar(at.ResolvedType) {
				// Non-scalar without JSON serialisation produces Go's %v
				// format, not valid JS. Type as 'unknown' to avoid misleading
				// type errors on what is essentially garbage output.
				warnings = append(warnings, fmt.Sprintf(
					"non-scalar type %s injected into <script> without JSON serialisation; output will be Go's %%v format, not valid JS (W008)",
					at.ResolvedType))
				find = marker
				repl = "(undefined! as unknown)"
			} else {
				find = marker
				tsType := GoTypeToTS(at.ResolvedType)
				repl = fmt.Sprintf("(undefined! as %s)", tsType)
			}

			replacements = append(replacements, replRecord{
				lineIdx: markerLine,
				find:    find,
				repl:    repl,
				origCol: origCol,
				origLen: origActionLen,
			})
		}

		// Apply replacements and build ColMap.
		colMap := make([][]colSpan, len(preLineOffsets))

		for _, r := range replacements {
			// Find the marker in the (possibly already mutated) scriptContent.
			idx := strings.Index(scriptContent, r.find)
			if idx < 0 {
				continue
			}

			// Find the start of the line containing this replacement by
			// scanning backwards for a newline in the mutated content.
			lineStart := 0
			if nlPos := strings.LastIndex(scriptContent[:idx], "\n"); nlPos >= 0 {
				lineStart = nlPos + 1
			}
			tsCol := idx - lineStart + 1

			colMap[r.lineIdx] = append(colMap[r.lineIdx], colSpan{
				TSCol:   tsCol,
				OrigCol: r.origCol,
				OrigLen: r.origLen,
				NewLen:  len(r.repl),
			})

			// Apply the replacement.
			scriptContent = scriptContent[:idx] + r.repl + scriptContent[idx+len(r.find):]
		}

		// TrimSpace may remove leading newlines and whitespace.
		// Adjust lineMap, colMap, and TSCol values accordingly.
		trimmed := strings.TrimSpace(scriptContent)
		leftTrimmed := scriptContent[:len(scriptContent)-len(strings.TrimLeft(scriptContent, " \t\n\r"))]
		leadingNewlines := strings.Count(leftTrimmed, "\n")
		if leadingNewlines > 0 && leadingNewlines < len(lineMap) {
			lineMap = lineMap[leadingNewlines:]
		}
		if leadingNewlines > 0 && leadingNewlines < len(colMap) {
			colMap = colMap[leadingNewlines:]
		}
		// Compute the column offset for the first line (leading spaces trimmed).
		colOffset := 0
		if len(colMap) > 0 {
			firstLineContent := scriptContent
			if lastNL := strings.LastIndex(leftTrimmed, "\n"); lastNL >= 0 {
				firstLineContent = scriptContent[lastNL+1:]
			}
			colOffset = len(firstLineContent) - len(strings.TrimLeft(firstLineContent, " \t"))
			// Adjust TSCol values on the first line to match trimmed coordinates.
			if colOffset > 0 {
				for i := range colMap[0] {
					colMap[0][i].TSCol -= colOffset
				}
			}
		}

		result.ScriptBlocks = append(result.ScriptBlocks, ScriptBlock{
			TemplateName: tree.Name,
			TemplateFile: tree.ParseName,
			StartLine:    sr.line,
			TypeScript:   trimmed,
			LineMap:      lineMap,
			ColMap:       colMap,
			ColOffset:    colOffset,
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

	// Emit branded type infrastructure if any block uses JSON branding.
	needsBrand := len(result.JSONBlocks) > 0 || len(result.HiddenInputBlocks) > 0
	if !needsBrand {
		for _, da := range result.DataAttrBlocks {
			for _, a := range da.Attrs {
				if a.IsJSON {
					needsBrand = true
					break
				}
			}
			if needsBrand {
				break
			}
		}
	}
	if !needsBrand {
		for _, m := range result.MetaBlocks {
			if m.IsJSON {
				needsBrand = true
				break
			}
		}
	}

	if needsBrand {
		buf.WriteString("declare const __brand: unique symbol;\n")
		buf.WriteString("type JSONString<T> = string & { [__brand]: T };\n")
		buf.WriteString("interface JSON { parse<T>(text: JSONString<T>): T; parse(text: string): any; }\n")
	}

	// Collect all getElementById overloads into a single Document interface.
	hasGetByID := len(result.JSONBlocks) > 0 || len(result.DataAttrBlocks) > 0 || len(result.HiddenInputBlocks) > 0
	hasQuerySelector := len(result.MetaBlocks) > 0

	if hasGetByID || hasQuerySelector {
		buf.WriteString("interface Document {\n")

		// JSON script block overloads.
		for _, jb := range result.JSONBlocks {
			fmt.Fprintf(&buf, "  getElementById(id: %q): (HTMLScriptElement & { textContent: JSONString<%s> }) | null;\n",
				jb.ID, jb.TSTyp)
		}

		// Data attribute overloads.
		for _, da := range result.DataAttrBlocks {
			fmt.Fprintf(&buf, "  getElementById(id: %q): (HTMLElement & { dataset:", da.ElementID)
			if len(da.Attrs) == 1 {
				a := da.Attrs[0]
				camel := dataToCamel(a.Name)
				if a.IsJSON {
					fmt.Fprintf(&buf, " { %s: JSONString<%s> }", camel, a.TSTyp)
				} else {
					fmt.Fprintf(&buf, " { %s: string }", camel)
				}
			} else {
				buf.WriteString(" {")
				for _, a := range da.Attrs {
					camel := dataToCamel(a.Name)
					if a.IsJSON {
						fmt.Fprintf(&buf, " %s: JSONString<%s>;", camel, a.TSTyp)
					} else {
						fmt.Fprintf(&buf, " %s: string;", camel)
					}
				}
				buf.WriteString(" }")
			}
			buf.WriteString(" }) | null;\n")
		}

		// Hidden input overloads.
		for _, hi := range result.HiddenInputBlocks {
			if hi.IsJSON {
				fmt.Fprintf(&buf, "  getElementById(id: %q): (HTMLInputElement & { value: JSONString<%s> }) | null;\n",
					hi.ElementID, hi.TSTyp)
			} else {
				fmt.Fprintf(&buf, "  getElementById(id: %q): (HTMLInputElement & { value: string }) | null;\n",
					hi.ElementID)
			}
		}

		// Meta tag querySelector overloads.
		for _, m := range result.MetaBlocks {
			if m.IsJSON {
				fmt.Fprintf(&buf, "  querySelector(selector: 'meta[name=%q]'): (HTMLMetaElement & { content: JSONString<%s> }) | null;\n",
					m.Name, m.TSTyp)
			} else {
				fmt.Fprintf(&buf, "  querySelector(selector: 'meta[name=%q]'): (HTMLMetaElement & { content: string }) | null;\n",
					m.Name)
			}
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

type dataAttrRange struct {
	elementID string
	attrs     map[string]string // data-* attr name (without prefix) -> raw value
}

type metaRange struct {
	name    string // the name attribute
	content string // raw content attribute value
}

type hiddenInputRange struct {
	elementID string
	value     string // raw value attribute
}

type parsedElements struct {
	jsRanges     []scriptRange
	jsonBlocks   []jsonBlockRange
	dataAttrs    []dataAttrRange
	metas        []metaRange
	hiddenInputs []hiddenInputRange
}

// parseHTMLElements uses the html tokenizer to locate script blocks,
// data-* attributes, <meta> tags, and <input type="hidden"> elements.
func parseHTMLElements(htmlStr string) parsedElements {
	var result parsedElements
	tokenizer := html.NewTokenizer(strings.NewReader(htmlStr))

	for {
		tt := tokenizer.Next()
		switch tt {
		case html.ErrorToken:
			return result

		case html.StartTagToken, html.SelfClosingTagToken:
			tn, hasAttr := tokenizer.TagName()
			tagName := string(tn)

			// Read all attributes if present.
			var attrs []html.Attribute
			if hasAttr {
				for {
					key, val, more := tokenizer.TagAttr()
					attrs = append(attrs, html.Attribute{Key: string(key), Val: string(val)})
					if !more {
						break
					}
				}
			}

			switch tagName {
			case "script":
				handleScriptTag(htmlStr, tokenizer, attrs, &result)
			case "meta":
				handleMetaTag(attrs, &result)
			case "input":
				handleInputTag(attrs, &result)
			default:
				if hasAttr {
					handleDataAttrs(tagName, attrs, &result)
				}
			}
		}
	}
}

func getAttr(attrs []html.Attribute, key string) string {
	for _, a := range attrs {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

const placeholderPrefix = "__TSEXTRACT_"

func handleScriptTag(htmlStr string, tokenizer *html.Tokenizer, attrs []html.Attribute, result *parsedElements) {
	scriptType := getAttr(attrs, "type")
	scriptID := getAttr(attrs, "id")

	// Read the content between <script> and </script>.
	tt := tokenizer.Next()
	if tt != html.TextToken {
		return
	}
	raw := tokenizer.Raw()
	text := string(raw)
	idx := strings.Index(htmlStr, text)
	if idx < 0 {
		return
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

func handleMetaTag(attrs []html.Attribute, result *parsedElements) {
	name := getAttr(attrs, "name")
	content := getAttr(attrs, "content")
	if name == "" || !strings.Contains(content, placeholderPrefix) {
		return
	}
	// Skip if name itself is a placeholder (dynamic meta name).
	if strings.Contains(name, placeholderPrefix) {
		return
	}
	result.metas = append(result.metas, metaRange{
		name:    name,
		content: content,
	})
}

func handleInputTag(attrs []html.Attribute, result *parsedElements) {
	inputType := getAttr(attrs, "type")
	if !strings.EqualFold(inputType, "hidden") {
		return
	}
	id := getAttr(attrs, "id")
	value := getAttr(attrs, "value")
	if id == "" || !strings.Contains(value, placeholderPrefix) {
		return
	}
	if strings.Contains(id, placeholderPrefix) {
		return
	}
	result.hiddenInputs = append(result.hiddenInputs, hiddenInputRange{
		elementID: id,
		value:     value,
	})
}

func handleDataAttrs(_ string, attrs []html.Attribute, result *parsedElements) {
	id := getAttr(attrs, "id")
	if id == "" || strings.Contains(id, placeholderPrefix) {
		return
	}
	dataAttrs := make(map[string]string)
	for _, a := range attrs {
		if strings.HasPrefix(a.Key, "data-") && strings.Contains(a.Val, placeholderPrefix) {
			dataAttrs[strings.TrimPrefix(a.Key, "data-")] = a.Val
		}
	}
	if len(dataAttrs) == 0 {
		return
	}
	result.dataAttrs = append(result.dataAttrs, dataAttrRange{
		elementID: id,
		attrs:     dataAttrs,
	})
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

// buildLineStarts returns the byte offsets where each line begins in the text.
// lineStarts[0] = 0 (line 1 starts at byte 0), lineStarts[1] = offset of line 2, etc.
func buildLineStarts(text string) []int {
	starts := []int{0}
	for i, c := range text {
		if c == '\n' {
			starts = append(starts, i+1)
		}
	}
	return starts
}

// byteOffsetToLine converts a byte offset in the template source to a 1-based
// line number using the precomputed lineStarts table.
func byteOffsetToLine(offset int, lineStarts []int) int {
	// Binary search for the largest lineStarts[i] <= offset.
	lo, hi := 0, len(lineStarts)-1
	for lo <= hi {
		mid := (lo + hi) / 2
		if lineStarts[mid] <= offset {
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	return lo // 1-based: lo is the count of starts <= offset
}

// buildLineMap creates a mapping from each line in a script content region
// (within the concatenated HTML) to the corresponding template source line.
// It returns a slice where lineMap[i] is the 1-based template line for
// TS output line i+1.
func buildLineMap(concatenated string, sourcePos []int, start, end int, lineStarts []int) []int {
	region := concatenated[start:end]
	var lineMap []int

	lineBegin := 0
	for i := 0; i <= len(region); i++ {
		if i == len(region) || region[i] == '\n' {
			// Map this line to the template source line of its first byte.
			srcOffset := 0
			if lineBegin+start < len(sourcePos) {
				srcOffset = sourcePos[lineBegin+start]
			}
			lineMap = append(lineMap, byteOffsetToLine(srcOffset, lineStarts))
			lineBegin = i + 1
		}
	}
	return lineMap
}

// dataToCamel converts a data attribute name (without "data-" prefix) to
// the camelCase form used by HTMLElement.dataset. E.g. "user-id" → "userId".
func dataToCamel(name string) string {
	parts := strings.Split(name, "-")
	for i := 1; i < len(parts); i++ {
		if len(parts[i]) > 0 {
			parts[i] = strings.ToUpper(parts[i][:1]) + parts[i][1:]
		}
	}
	return strings.Join(parts, "")
}

type placeholder struct {
	index  int // index in flat list
	action *parse.ActionNode
}

// resolveAttrPlaceholder finds the first placeholder in an attribute value
// and returns its index, resolved ActionTypes, and whether it was found.
func resolveAttrPlaceholder(attrVal string, placeholders []placeholder, actions map[*parse.ActionNode]ActionTypes) (int, ActionTypes, bool) {
	for j, ph := range placeholders {
		marker := fmt.Sprintf("__TSEXTRACT_%d__", j)
		if !strings.Contains(attrVal, marker) {
			continue
		}
		at, ok := actions[ph.action]
		if !ok {
			continue
		}
		return j, at, true
	}
	return -1, ActionTypes{}, false
}

// isJSONPiped reports whether an action's type signature suggests it was
// piped through a JSON marshaller (InputType differs from ResolvedType and
// ResolvedType is string).
func isJSONPiped(at ActionTypes) bool {
	if at.InputType == nil || at.ResolvedType == nil {
		return false
	}
	if at.InputType == at.ResolvedType {
		return false
	}
	basic, ok := at.ResolvedType.(*types.Basic)
	return ok && basic.Kind() == types.String
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
