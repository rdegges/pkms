package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/rdegges/pkms/internal/profile"
)

func newProfileCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "profile",
		Short: "Inspect and eject organization profiles",
	}

	list := &cobra.Command{
		Use:   "list",
		Short: "List built-in profiles",
		RunE: func(cmd *cobra.Command, args []string) error {
			for _, name := range profile.Builtins() {
				p, err := profile.Load(name)
				if err != nil {
					return err
				}
				fmt.Fprintf(cmd.OutOrStdout(), "%-10s %s\n", p.Name, p.Description)
			}
			return nil
		},
	}

	eject := &cobra.Command{
		Use:   "eject <name> <dir>",
		Short: "Copy a built-in profile to a directory for customization",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := profile.Load(args[0])
			if err != nil {
				return err
			}
			if entries, err := os.ReadDir(args[1]); err == nil && len(entries) > 0 {
				return fmt.Errorf("%s is not empty", args[1])
			}
			if err := p.Eject(args[1]); err != nil {
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(),
				"profile %q ejected to %s\npoint a vault at it: profile = %q in config\n",
				p.Name, args[1], args[1])
			return nil
		},
	}

	cmd.AddCommand(list, eject, newProfileShowCmd())
	return cmd
}

// profileView is the frozen `profile show --json` shape (SPEC §32.2): a
// profile's complete STATIC manifest, so a vault-agnostic agent learns
// folder templates, note types (in classification order), and the
// ingest/capture mapping from data instead of hard-coding them.
type profileView struct {
	Name          string                    `json:"name"`
	Description   string                    `json:"description"`
	SchemaVersion int                       `json:"schema_version"`
	Attachments   string                    `json:"attachments"`
	Scaffold      []string                  `json:"scaffold"`
	RootFiles     []string                  `json:"root_files"`
	Ingest        ingestView                `json:"ingest"`
	Indexes       []indexView               `json:"indexes"`
	Types         []typeView                `json:"types"`
	Lint          map[string]map[string]any `json:"lint"`
}

type ingestView struct {
	Clip  string `json:"clip"`
	Asset string `json:"asset"`
}

type indexView struct {
	File   string `json:"file"`
	Lists  string `json:"lists"`
	Policy string `json:"policy"`
}

type typeView struct {
	Name          string   `json:"name"`
	Scope         []string `json:"scope"`
	RequireAnyKey []string `json:"require_any_key"`
	Folder        string   `json:"folder"`
	Filename      string   `json:"filename"`
	Template      string   `json:"template"`
	// Schema is the note type's JSON Schema inlined byte-faithfully (never
	// re-marshaled — an agent must validate against exactly what the writer
	// enforces). Null when the type declares no schema.
	Schema json.RawMessage `json:"schema"`
}

func newProfileShowCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "show [name]",
		Short: "Show a profile's full structure (folders, note types, schemas)",
		Long: `Print a profile's complete static manifest: scaffold folders, the
ingest/capture mapping, index rules, and every note type (in
classification order) with its placement templates and JSON Schema. This
is the vault-agnostic seam — agents read structure here instead of
assuming folder names. Pass a built-in/ejected profile name, or --vault to
show the profile a vault uses.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			prof, err := resolveShownProfile(cmd, args)
			if err != nil {
				return err
			}
			return renderProfileView(cmd, prof, jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "machine-readable output")
	return cmd
}

// resolveShownProfile picks the profile: a positional name XOR --vault.
func resolveShownProfile(cmd *cobra.Command, args []string) (*profile.Profile, error) {
	vaultName, _ := cmd.Flags().GetString("vault")
	if vaultName == "" {
		vaultName, _ = cmd.Root().PersistentFlags().GetString("vault")
	}
	if len(args) == 1 {
		if vaultName != "" {
			return nil, fmt.Errorf("pass a profile name OR --vault, not both")
		}
		return profile.Load(args[0])
	}
	cfg, err := loadConfig(cmd)
	if err != nil {
		return nil, err
	}
	v, err := selectedVault(cmd, cfg)
	if err != nil {
		return nil, err
	}
	return profile.Load(v.Profile)
}

func buildProfileView(p *profile.Profile) (*profileView, error) {
	view := &profileView{
		Name:          p.Name,
		Description:   p.Description,
		SchemaVersion: p.SchemaVersion,
		Attachments:   p.Attachments,
		Scaffold:      nonNil(p.Scaffold),
		RootFiles:     nonNil(p.RootFiles),
		Ingest:        ingestView{Clip: p.Ingest.Clip, Asset: p.Ingest.Asset},
		Indexes:       []indexView{},
		Types:         []typeView{},
		Lint:          nonNilLint(p.Lint),
	}
	for _, ix := range p.Indexes {
		view.Indexes = append(view.Indexes, indexView{File: ix.File, Lists: ix.Lists, Policy: ix.Policy})
	}
	for _, t := range p.Types {
		schemaBytes, err := p.SchemaBytes(t.Name)
		if err != nil {
			return nil, fmt.Errorf("type %q: %w", t.Name, err)
		}
		var raw json.RawMessage
		if schemaBytes != nil {
			raw = json.RawMessage(schemaBytes) // byte-faithful, never re-marshaled
		}
		view.Types = append(view.Types, typeView{
			Name:          t.Name,
			Scope:         nonNil(t.Scope),
			RequireAnyKey: nonNil(t.RequireAnyKey),
			Folder:        t.Folder,
			Filename:      t.Filename,
			Template:      t.Template,
			Schema:        raw,
		})
	}
	return view, nil
}

func renderProfileView(cmd *cobra.Command, prof *profile.Profile, jsonOut bool) error {
	out := cmd.OutOrStdout()
	if jsonOut {
		// Serialize through the shared producer the `pkms mcp` profile_show
		// tool also uses (§32.6). HTML escaping is off there so a note-type
		// schema's raw bytes survive verbatim (an ejected profile's
		// `^[^<>]*$` pattern must not become <).
		b, err := profileJSON(prof)
		if err != nil {
			return err
		}
		_, err = out.Write(b)
		return err
	}
	view, err := buildProfileView(prof)
	if err != nil {
		return err
	}
	fmt.Fprintf(out, "%s — %s (schema v%d)\n", view.Name, view.Description, view.SchemaVersion)
	fmt.Fprintf(out, "attachments: %s\n", orNone(view.Attachments))
	fmt.Fprintf(out, "scaffold: %s\n", joinOrNone(view.Scaffold))
	fmt.Fprintf(out, "root_files: %s\n", joinOrNone(view.RootFiles))
	fmt.Fprintf(out, "ingest: clip=%s asset=%s\n", orNone(view.Ingest.Clip), orNone(view.Ingest.Asset))
	if len(view.Lint) > 0 {
		rules := make([]string, 0, len(view.Lint))
		for r := range view.Lint {
			rules = append(rules, r)
		}
		sort.Strings(rules)
		fmt.Fprintf(out, "lint: %s\n", joinOrNone(rules))
	}
	fmt.Fprintf(out, "\ntypes (classification order):\n")
	for _, t := range view.Types {
		schema := "no schema"
		if t.Schema != nil {
			schema = "schema"
		}
		fmt.Fprintf(out, "  %-14s folder=%-28s %s\n", t.Name, t.Folder, schema)
	}
	if len(view.Indexes) > 0 {
		fmt.Fprintf(out, "\nindexes:\n")
		for _, ix := range view.Indexes {
			fmt.Fprintf(out, "  %s lists %s (%s)\n", ix.File, ix.Lists, ix.Policy)
		}
	}
	return nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// nonNilLint keeps the frozen shape's `lint` key stable: a profile with no
// [lint] table emits `{}`, never omits the key (SPEC §32.2 frozen shape).
func nonNilLint(m map[string]map[string]any) map[string]map[string]any {
	if m == nil {
		return map[string]map[string]any{}
	}
	return m
}

func orNone(s string) string {
	if s == "" {
		return "(none)"
	}
	return s
}

func joinOrNone(s []string) string {
	if len(s) == 0 {
		return "(none)"
	}
	return strings.Join(s, ", ")
}
