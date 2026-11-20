package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"

	"contextdrop.dev/context-drop/internal/mods"
	"contextdrop.dev/context-drop/internal/pool"
	"github.com/spf13/cobra"
)

// orgMembership is the membership commands that only work in an org need.
func orgMembership() (pool.Membership, error) {
	m, isJoined, err := joined()
	if err != nil {
		return m, err
	}
	if !isJoined {
		return m, errors.New("this machine has not joined an org; run `context-drop join TOKEN` first")
	}
	return m, nil
}

func newDeployCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "deploy FILE",
		Short: "Deploy a mod (a TypeScript file) to the org, or replace it",
		Long: "Upload a mod and make it live for the whole org at once, replacing any mod of the same name. A mod is\n" +
			"one TypeScript file that exports `mod({ name, description, input, can, run })` from \"@contextdrop/mod\";\n" +
			"see docs/mods.md.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			m, err := orgMembership()
			if err != nil {
				return err
			}
			source, err := os.ReadFile(args[0])
			if err != nil {
				return err
			}
			var res mods.Summary
			req := map[string]any{"source": string(source), "agent": callerAgent(cmd.Context())}
			if err := (pool.Client{Membership: m}).Post(cmd.Context(), "mods/deploy", req, &res); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Deployed %s (can: %s). Call it: context-drop %s\n", res.Name,
				firstNonEmptyString(strings.Join(res.Can, ", "), "nothing"), res.Name)
			return nil
		},
	}
}

func newModsCommand() *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "mods",
		Short: "List the org's mods with their capabilities and author",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			m, err := orgMembership()
			if err != nil {
				return err
			}
			var list []mods.Summary
			if err := (pool.Client{Membership: m}).Post(cmd.Context(), "mods", struct{}{}, &list); err != nil {
				return err
			}
			if asJSON {
				return writeJSON(cmd.OutOrStdout(), list)
			}
			if len(list) == 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "Org %s has no mods yet. Write one and run `context-drop deploy FILE`.\n", m.Org)
				return nil
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "MOD\tCAN\tAUTHOR\tDEPLOYED\tDESCRIPTION")
			for _, s := range list {
				fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", s.Name, firstNonEmptyString(strings.Join(s.Can, ","), "-"),
					s.Author, ago(s.Deployed), truncateRunes(s.Description, 60))
			}
			return w.Flush()
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "print JSON")
	return cmd
}

func newCallCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "call MOD [--FIELD VALUE]... [--json INPUT]",
		Short: "Call a mod; `context-drop MOD ...` does the same",
		Long: "Run a mod and print what it returns: text as is, a list of objects as a table, anything else as\n" +
			"JSON. Input comes from flags named after the mod's input fields (coerced to their types), or whole\n" +
			"from --json. One positional argument fills the mod's only required field. --print-json prints raw JSON.",
		DisableFlagParsing: true,
		Args:               cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return callMod(cmd, args[0], args[1:])
		},
	}
}

func callMod(cmd *cobra.Command, name string, args []string) error {
	for _, a := range args {
		if a == "-h" || a == "--help" {
			return modHelp(cmd, name)
		}
	}
	m, err := orgMembership()
	if err != nil {
		return err
	}
	c := pool.Client{Membership: m}
	schema, err := modSchema(cmd, c, name)
	if err != nil {
		return err
	}
	input, raw, err := modInput(schema, args)
	if err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	var out struct {
		Output json.RawMessage `json:"output"`
	}
	if err := c.Post(cmd.Context(), "mods/"+name+"/call", map[string]any{"input": input, "agent": callerAgent(cmd.Context())}, &out); err != nil {
		return err
	}
	return printOutput(cmd.OutOrStdout(), out.Output, raw)
}

type inputSchema struct {
	Properties map[string]struct {
		Type        string `json:"type"`
		Description string `json:"description"`
		Default     any    `json:"default"`
	} `json:"properties"`
	Required []string `json:"required"`
}

func modSchema(cmd *cobra.Command, c pool.Client, name string) (inputSchema, error) {
	var list []mods.Summary
	if err := c.Post(cmd.Context(), "mods", struct{}{}, &list); err != nil {
		return inputSchema{}, err
	}
	for _, s := range list {
		if s.Name == name {
			var schema inputSchema
			_ = json.Unmarshal(s.Input, &schema)
			return schema, nil
		}
	}
	return inputSchema{}, fmt.Errorf("unknown command or mod %q; `context-drop mods` lists the org's mods", name)
}

func modHelp(cmd *cobra.Command, name string) error {
	m, err := orgMembership()
	if err != nil {
		return err
	}
	var list []mods.Summary
	if err := (pool.Client{Membership: m}).Post(cmd.Context(), "mods", struct{}{}, &list); err != nil {
		return err
	}
	for _, s := range list {
		if s.Name != name {
			continue
		}
		var schema inputSchema
		_ = json.Unmarshal(s.Input, &schema)
		fmt.Fprintf(cmd.OutOrStdout(), "%s: %s\n\nUsage: context-drop %s", s.Name, s.Description, s.Name)
		fields := make([]string, 0, len(schema.Properties))
		for k := range schema.Properties {
			fields = append(fields, k)
		}
		sort.Strings(fields)
		for _, f := range fields {
			fmt.Fprintf(cmd.OutOrStdout(), " [--%s %s]", f, schema.Properties[f].Type)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "\n\nBy %s, can: %s\n", s.Author, firstNonEmptyString(strings.Join(s.Can, ", "), "nothing"))
		for _, f := range fields {
			p := schema.Properties[f]
			line := fmt.Sprintf("  --%s %s", f, p.Type)
			if p.Description != "" {
				line += "  " + p.Description
			}
			if p.Default != nil {
				line += fmt.Sprintf(" (default %v)", p.Default)
			}
			fmt.Fprintln(cmd.OutOrStdout(), line)
		}
		return nil
	}
	return fmt.Errorf("no mod %q", name)
}

// modInput turns CLI arguments into a mod's input, coercing flag values to the schema's types.
func modInput(schema inputSchema, args []string) (map[string]any, bool, error) {
	input := map[string]any{}
	raw := false
	var positional []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !strings.HasPrefix(a, "--") {
			positional = append(positional, a)
			continue
		}
		key, val, hasVal := strings.Cut(strings.TrimPrefix(a, "--"), "=")
		switch key {
		case "print-json":
			raw = true
			continue
		case "json":
			if !hasVal {
				if i+1 >= len(args) {
					return nil, false, errors.New("--json needs a value")
				}
				i++
				val = args[i]
			}
			if err := json.Unmarshal([]byte(val), &input); err != nil {
				return nil, false, fmt.Errorf("--json: %w", err)
			}
			continue
		}
		prop, known := schema.Properties[key]
		if !known {
			return nil, false, fmt.Errorf("unknown flag --%s", key)
		}
		if !hasVal {
			if prop.Type == "boolean" {
				input[key] = true
				continue
			}
			if i+1 >= len(args) {
				return nil, false, fmt.Errorf("--%s needs a value", key)
			}
			i++
			val = args[i]
		}
		v, err := coerce(prop.Type, val)
		if err != nil {
			return nil, false, fmt.Errorf("--%s: %w", key, err)
		}
		if prop.Type == "array" {
			prev, _ := input[key].([]any)
			input[key] = append(prev, v.([]any)...)
		} else {
			input[key] = v
		}
	}
	if len(positional) > 0 {
		var missing []string
		for _, r := range schema.Required {
			if _, ok := input[r]; !ok {
				missing = append(missing, r)
			}
		}
		if len(missing) != 1 {
			return nil, false, fmt.Errorf("unexpected argument %q; pass input as --FIELD VALUE", positional[0])
		}
		v, err := coerce(schema.Properties[missing[0]].Type, strings.Join(positional, " "))
		if err != nil {
			return nil, false, err
		}
		input[missing[0]] = v
	}
	return input, raw, nil
}

func coerce(typ, val string) (any, error) {
	switch typ {
	case "number":
		return strconv.ParseFloat(val, 64)
	case "boolean":
		return strconv.ParseBool(val)
	case "array":
		var list []any
		if strings.HasPrefix(strings.TrimSpace(val), "[") {
			err := json.Unmarshal([]byte(val), &list)
			return list, err
		}
		return []any{val}, nil
	case "object":
		var obj map[string]any
		err := json.Unmarshal([]byte(val), &obj)
		return obj, err
	}
	return val, nil
}

// printOutput prints a mod's result: text as is, a list of objects as a table, anything else as JSON.
func printOutput(w io.Writer, out json.RawMessage, raw bool) error {
	if raw {
		_, err := fmt.Fprintln(w, string(out))
		return err
	}
	var s string
	if json.Unmarshal(out, &s) == nil {
		_, err := fmt.Fprintln(w, strings.TrimRight(s, "\n"))
		return err
	}
	var rows []map[string]any
	if json.Unmarshal(out, &rows) == nil && len(rows) > 0 {
		var cols []string
		seen := map[string]bool{}
		for _, k := range orderedKeys(mustArrayFirst(out)) {
			cols, seen[k] = append(cols, k), true
		}
		for _, r := range rows {
			extra := []string{}
			for k := range r {
				if !seen[k] {
					extra, seen[k] = append(extra, k), true
				}
			}
			sort.Strings(extra)
			cols = append(cols, extra...)
		}
		tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
		fmt.Fprintln(tw, strings.ToUpper(strings.Join(cols, "\t")))
		for _, r := range rows {
			cells := make([]string, len(cols))
			for i, c := range cols {
				cells[i] = cell(r[c])
			}
			fmt.Fprintln(tw, strings.Join(cells, "\t"))
		}
		return tw.Flush()
	}
	var v any
	if err := json.Unmarshal(out, &v); err != nil {
		_, err := fmt.Fprintln(w, string(out))
		return err
	}
	if v == nil {
		return nil
	}
	return writeJSON(w, v)
}

func cell(v any) string {
	switch x := v.(type) {
	case nil:
		return "-"
	case string:
		return truncateRunes(strings.Join(strings.Fields(x), " "), 70)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	default:
		b, _ := json.Marshal(x)
		return truncateRunes(string(b), 70)
	}
}

func mustArrayFirst(out json.RawMessage) json.RawMessage {
	var arr []json.RawMessage
	if json.Unmarshal(out, &arr) == nil && len(arr) > 0 {
		return arr[0]
	}
	return nil
}

// orderedKeys returns an object's keys in the order they appear.
func orderedKeys(obj json.RawMessage) []string {
	dec := json.NewDecoder(strings.NewReader(string(obj)))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil
	}
	var keys []string
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return keys
		}
		keys = append(keys, t.(string))
		var skip json.RawMessage
		if err := dec.Decode(&skip); err != nil {
			return keys
		}
	}
	return keys
}

// ModCallArgs rewrites `context-drop NAME ...` into `context-drop call NAME ...` when NAME is not a
// built-in command and this machine has joined an org, so mods read like commands.
func ModCallArgs(root *cobra.Command, args []string) []string {
	if len(args) == 0 || strings.HasPrefix(args[0], "-") || args[0] == "help" {
		return args
	}
	for _, c := range root.Commands() {
		if c.Name() == args[0] || c.HasAlias(args[0]) {
			return args
		}
	}
	if _, isJoined, err := joined(); err != nil || !isJoined {
		return args
	}
	return append([]string{"call"}, args...)
}
