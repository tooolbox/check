package tsextract

import (
	"fmt"
	"go/types"
	"reflect"
	"strings"
)

// GoTypeToTS converts a Go types.Type to its TypeScript representation.
// The mapping follows JSON marshalling semantics since template-injected
// values are typically serialised to JSON before reaching JavaScript.
func GoTypeToTS(typ types.Type) string {
	return goTypeToTS(typ, nil)
}

func goTypeToTS(typ types.Type, seen map[*types.Named]bool) string {
	if typ == nil {
		return "unknown"
	}
	if seen == nil {
		seen = make(map[*types.Named]bool)
	}

	switch t := typ.(type) {
	case *types.Named:
		// Detect recursive types.
		if seen[t] {
			return "unknown"
		}
		seen[t] = true
		defer delete(seen, t)

		// Special-case well-known types.
		obj := t.Obj()
		if obj.Pkg() != nil {
			path := obj.Pkg().Path()
			switch {
			case path == "time" && obj.Name() == "Time":
				return "string" // JSON marshals to RFC 3339
			case path == "encoding/json" && obj.Name() == "RawMessage":
				return "unknown"
			case path == "encoding/json" && obj.Name() == "Number":
				return "number | string"
			}
		}
		return goTypeToTS(t.Underlying(), seen)

	case *types.Basic:
		return basicToTS(t)

	case *types.Pointer:
		inner := goTypeToTS(t.Elem(), seen)
		return inner + " | null"

	case *types.Slice:
		// []byte → unknown (JSON marshals to base64 string, but could be anything)
		if isBasicKind(t.Elem(), types.Byte) {
			return "unknown"
		}
		return "Array<" + goTypeToTS(t.Elem(), seen) + ">"

	case *types.Array:
		return "Array<" + goTypeToTS(t.Elem(), seen) + ">"

	case *types.Map:
		key := goTypeToTS(t.Key(), seen)
		val := goTypeToTS(t.Elem(), seen)
		return fmt.Sprintf("Record<%s, %s>", key, val)

	case *types.Struct:
		return structToTS(t, seen)

	case *types.Interface:
		return "unknown"

	case *types.Signature:
		return "unknown" // functions don't serialise to JSON

	case *types.Chan:
		return "unknown"

	default:
		return "unknown"
	}
}

func basicToTS(t *types.Basic) string {
	info := t.Info()
	switch {
	case info&types.IsString != 0:
		return "string"
	case info&types.IsBoolean != 0:
		return "boolean"
	case info&types.IsNumeric != 0:
		return "number"
	default:
		return "unknown"
	}
}

func isBasicKind(typ types.Type, kind types.BasicKind) bool {
	b, ok := typ.(*types.Basic)
	return ok && b.Kind() == kind
}

func structToTS(t *types.Struct, seen map[*types.Named]bool) string {
	var fields []string
	for i := range t.NumFields() {
		f := t.Field(i)
		tag := reflect.StructTag(t.Tag(i))

		jsonTag := tag.Get("json")
		name, opts := parseJSONTag(jsonTag)

		// Skip fields that JSON would skip.
		if name == "-" {
			continue
		}
		if !f.Exported() && name == "" {
			continue
		}

		// Use json name if provided, otherwise the Go field name.
		if name == "" {
			name = f.Name()
		}

		tsType := goTypeToTS(f.Type(), seen)
		optional := ""
		if strings.Contains(opts, "omitempty") {
			optional = "?"
		}

		fields = append(fields, fmt.Sprintf("%s%s: %s", name, optional, tsType))
	}
	if len(fields) == 0 {
		return "{}"
	}
	return "{ " + strings.Join(fields, "; ") + " }"
}

// parseJSONTag splits a json struct tag into the name and comma-separated options.
func parseJSONTag(tag string) (string, string) {
	if tag == "" {
		return "", ""
	}
	name, opts, _ := strings.Cut(tag, ",")
	return name, opts
}
