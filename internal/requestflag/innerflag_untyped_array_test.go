package requestflag

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v3"
)

// An untyped outer flag (Flag[any], used for nullable array-of-objects schemas) that is
// given a JSON or YAML array literal holds a []any, not a []map[string]any. Inner flags
// must merge into the trailing element the same way they do for a typed slice flag.
func TestInnerFlagAfterUntypedArrayLiteral(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name string
		args []string
		want string
	}{
		{
			"merge into the trailing element",
			[]string{"--entry", `[{"name":"earlier"}]`, "--entry.description", "details"},
			`{"entries":[{"name":"earlier","description":"details"}]}`,
		},
		{
			"repeated field starts another element",
			[]string{"--entry", `[{"name":"earlier"}]`, "--entry.name", "demo"},
			`{"entries":[{"name":"earlier"},{"name":"demo"}]}`,
		},
		{
			"two inner fields fill an empty element",
			[]string{"--entry", "[{}]", "--entry.name", "demo", "--entry.description", "details"},
			`{"entries":[{"name":"demo","description":"details"}]}`,
		},
		{
			"yaml flow array",
			[]string{"--entry", "- {name: earlier}", "--entry.description", "details"},
			`{"entries":[{"name":"earlier","description":"details"}]}`,
		},
		{
			"non-object element starts a new one",
			[]string{"--entry", `["plain"]`, "--entry.name", "demo"},
			`{"entries":["plain",{"name":"demo"}]}`,
		},
		{
			"null element starts a new one",
			[]string{"--entry", `[null]`, "--entry.name", "demo"},
			`{"entries":[null,{"name":"demo"}]}`,
		},
		{
			"empty array",
			[]string{"--entry", "[]", "--entry.name", "demo"},
			`{"entries":[{"name":"demo"}]}`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			outer := &Flag[any]{Name: "entry", BodyPath: "entries"}
			var body []byte
			command := WithInnerFlags(cli.Command{
				Name:  "test",
				Flags: []cli.Flag{outer},
				Action: func(_ context.Context, cmd *cli.Command) error {
					var err error
					body, err = json.Marshal(ExtractRequestContents(cmd).Body)
					return err
				},
			}, map[string][]HasOuterFlag{
				"entry": {
					&InnerFlag[string]{Name: "entry.name", InnerField: "name", OuterIsArrayOfObjects: true},
					&InnerFlag[string]{Name: "entry.description", InnerField: "description", OuterIsArrayOfObjects: true},
				},
			})

			require.NoError(t, command.Run(context.Background(), append([]string{"test"}, tt.args...)))
			assert.JSONEq(t, tt.want, string(body))
		})
	}
}

// Same sequence driven directly, mirroring the existing coverage for a `null` element.
func TestInnerFlagAfterUntypedArrayLiteralDirect(t *testing.T) {
	t.Parallel()

	outer := &Flag[any]{Name: "entry"}
	require.NoError(t, outer.PreParse())
	require.NoError(t, outer.Set(outer.Name, `[{"name":"earlier"}]`))

	name := &InnerFlag[string]{
		Name: "entry.name", InnerField: "name", OuterFlag: outer, OuterIsArrayOfObjects: true,
	}
	description := &InnerFlag[string]{
		Name: "entry.description", InnerField: "description", OuterFlag: outer, OuterIsArrayOfObjects: true,
	}
	require.NoError(t, name.Set(name.Name, "demo"))
	require.NoError(t, description.Set(description.Name, "details"))

	assert.Equal(t,
		[]any{map[string]any{"name": "earlier"}, map[string]any{"name": "demo", "description": "details"}},
		outer.Get(),
	)
}

// An untyped outer flag set to text or another scalar has no field for an inner
// flag to set, so the inner value is reported instead of being dropped.
func TestInnerFlagAfterUntypedScalar(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"plain", "5", "null"} {
		t.Run(value, func(t *testing.T) {
			t.Parallel()

			outer := &Flag[any]{Name: "entry", BodyPath: "entries"}
			command := WithInnerFlags(cli.Command{
				Name:   "test",
				Flags:  []cli.Flag{outer},
				Action: func(context.Context, *cli.Command) error { return nil },
			}, map[string][]HasOuterFlag{
				"entry": {
					&InnerFlag[string]{Name: "entry.name", InnerField: "name", OuterIsArrayOfObjects: true},
				},
			})

			err := command.Run(context.Background(), []string{"test", "--entry", value, "--entry.name", "demo"})
			assert.ErrorContains(t, err, "cannot set --entry.name because --entry is not an object")
		})
	}
}

// Text that YAML reads as a mapping is sent as text on its own, but an inner
// flag needs an object, so the inner value is added to that mapping.
func TestInnerFlagAfterUntypedTextMapping(t *testing.T) {
	t.Parallel()

	outer := &Flag[any]{Name: "entry", BodyPath: "entries"}
	var body any
	command := WithInnerFlags(cli.Command{
		Name:  "test",
		Flags: []cli.Flag{outer},
		Action: func(_ context.Context, cmd *cli.Command) error {
			body = ExtractRequestContents(cmd).Body
			return nil
		},
	}, map[string][]HasOuterFlag{
		"entry": {
			&InnerFlag[string]{Name: "entry.name", InnerField: "name", OuterIsArrayOfObjects: true},
		},
	})

	require.NoError(t, command.Run(context.Background(), []string{"test", "--entry", "role: user", "--entry.name", "demo"}))
	got, err := json.Marshal(body)
	require.NoError(t, err)
	assert.JSONEq(t, `{"entries": {"role": "user", "name": "demo"}}`, string(got))
}

// Text or null set on an untyped outer flag takes precedence over piped inner
// fields, just as other explicit flags take precedence over piped data. Text
// that YAML reads as a mapping is an object, so piped fields fill in the rest.
func TestPipedInnerFieldAfterUntypedScalar(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct{ value, want string }{
		{"null", `null`},
		{"plain", `"plain"`},
		{"role: user", `{"role": "user", "name": "piped"}`},
	} {
		t.Run(tt.value, func(t *testing.T) {
			t.Parallel()

			outer := &Flag[any]{Name: "entry", BodyPath: "entries"}
			var body any
			command := WithInnerFlags(cli.Command{
				Name:  "test",
				Flags: []cli.Flag{outer},
				Action: func(_ context.Context, cmd *cli.Command) error {
					piped := map[string]any{"entries": map[string]any{"name": "piped"}}
					if err := ApplyStdinDataToFlags(cmd, piped); err != nil {
						return err
					}
					body = ExtractRequestContents(cmd).Body
					return nil
				},
			}, map[string][]HasOuterFlag{
				"entry": {
					&InnerFlag[string]{Name: "entry.name", InnerField: "name", OuterIsArrayOfObjects: true},
				},
			})

			require.NoError(t, command.Run(context.Background(), []string{"test", "--entry", tt.value}))
			got, err := json.Marshal(body)
			require.NoError(t, err)
			assert.JSONEq(t, `{"entries": `+tt.want+`}`, string(got))
		})
	}
}
