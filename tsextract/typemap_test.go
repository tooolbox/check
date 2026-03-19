package tsextract

import (
	"go/types"
	"testing"
)

func TestGoTypeToTS(t *testing.T) {
	tests := []struct {
		name string
		typ  types.Type
		want string
	}{
		{"string", types.Typ[types.String], "string"},
		{"int", types.Typ[types.Int], "number"},
		{"int64", types.Typ[types.Int64], "number"},
		{"float64", types.Typ[types.Float64], "number"},
		{"bool", types.Typ[types.Bool], "boolean"},
		{"uint", types.Typ[types.Uint], "number"},
		{"byte", types.Typ[types.Byte], "number"},

		{"nil", nil, "unknown"},

		{"string slice", types.NewSlice(types.Typ[types.String]), "Array<string>"},
		{"int slice", types.NewSlice(types.Typ[types.Int]), "Array<number>"},
		{"byte slice", types.NewSlice(types.Typ[types.Byte]), "unknown"},

		{"array", types.NewArray(types.Typ[types.String], 3), "Array<string>"},

		{"map string string", types.NewMap(types.Typ[types.String], types.Typ[types.String]), "Record<string, string>"},
		{"map string int", types.NewMap(types.Typ[types.String], types.Typ[types.Int]), "Record<string, number>"},

		{"pointer", types.NewPointer(types.Typ[types.String]), "string | null"},

		{"empty interface", types.NewInterfaceType(nil, nil), "unknown"},

		{"struct", types.NewStruct(
			[]*types.Var{
				types.NewField(0, nil, "Name", types.Typ[types.String], false),
				types.NewField(0, nil, "Age", types.Typ[types.Int], false),
			},
			[]string{"", ""},
		), "{ Name: string; Age: number }"},

		{"struct with json tags", types.NewStruct(
			[]*types.Var{
				types.NewField(0, nil, "Name", types.Typ[types.String], false),
				types.NewField(0, nil, "Email", types.Typ[types.String], false),
			},
			[]string{`json:"name"`, `json:"email,omitempty"`},
		), `{ name: string; email?: string }`},

		{"struct json skip", types.NewStruct(
			[]*types.Var{
				types.NewField(0, nil, "Name", types.Typ[types.String], false),
				types.NewField(0, nil, "Internal", types.Typ[types.String], false),
			},
			[]string{"", `json:"-"`},
		), "{ Name: string }"},

		{"nested slice of structs", types.NewSlice(types.NewStruct(
			[]*types.Var{
				types.NewField(0, nil, "ID", types.Typ[types.Int], false),
			},
			[]string{""},
		)), "Array<{ ID: number }>"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GoTypeToTS(tt.typ)
			if got != tt.want {
				t.Errorf("GoTypeToTS() = %q, want %q", got, tt.want)
			}
		})
	}
}
