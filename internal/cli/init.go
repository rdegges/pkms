package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/rdegges/pkms/internal/config"
	"github.com/rdegges/pkms/internal/gitx"
	"github.com/rdegges/pkms/internal/profile"
)

// defaultGitignore per SPEC §9.
const defaultGitignore = `.obsidian/workspace*
.DS_Store
*.sync-conflict-*
.trash/
`

func newInitCmd() *cobra.Command {
	var (
		path        string
		name        string
		profileName string
		adopt       bool
		dryRun      bool
	)
	cmd := &cobra.Command{
		Use:   "init --path <dir>",
		Short: "Scaffold a vault from a profile and register it",
		Long: `Creates the profile's folder layout, initializes a git repository for
snapshots, and registers the vault in the pkms config. Idempotent: re-running
only fills gaps and never overwrites existing files. Use --adopt to register
an existing non-empty vault without scaffolding over its content.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runInit(cmd, path, name, profileName, adopt, dryRun)
		},
	}
	cmd.Flags().StringVar(&path, "path", "", "vault directory (created if missing)")
	cmd.Flags().StringVar(&name, "name", "", "vault name for config (default: directory basename)")
	cmd.Flags().StringVar(&profileName, "profile", "", "profile name or directory (default: para)")
	cmd.Flags().BoolVar(&adopt, "adopt", false, "register an existing non-empty directory (skip scaffolding conflicts check)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the plan without changing anything")
	_ = cmd.MarkFlagRequired("path")
	return cmd
}

func runInit(cmd *cobra.Command, path, name, profileName string, adopt, dryRun bool) error {
	out := cmd.OutOrStdout()

	vaultPath, err := config.ExpandPath(path)
	if err != nil {
		return err
	}
	cfg, err := loadConfig(cmd)
	if err != nil {
		return err
	}

	// An already-registered path means "fill gaps", not an error.
	var registered *config.Vault
	for i := range cfg.Vaults {
		if cfg.Vaults[i].Path == vaultPath {
			registered = &cfg.Vaults[i]
		}
	}

	if name == "" {
		if registered != nil {
			name = registered.Name
		} else {
			name = slugify(filepath.Base(vaultPath))
		}
	}
	if registered == nil {
		if v, err := cfg.Vault(name); err == nil && v.Path != vaultPath {
			return fmt.Errorf("vault name %q is already registered for %s (pick --name)", name, v.Path)
		}
	}

	if profileName == "" {
		if registered != nil {
			profileName = registered.Profile
		} else if cfg.Defaults.Profile != "" {
			profileName = cfg.Defaults.Profile
		} else {
			profileName = "para"
		}
	}
	prof, err := profile.Load(profileName)
	if err != nil {
		return err
	}

	// Non-empty unregistered dirs need explicit consent.
	if entries, err := os.ReadDir(vaultPath); err == nil && len(entries) > 0 && registered == nil && !adopt {
		return fmt.Errorf("%s is not empty; use --adopt to register it as a vault anyway", vaultPath)
	}

	if _, err := gitx.LookPath(); err != nil {
		return fmt.Errorf("pkms init needs git for snapshots (brew install git / apt install git)")
	}

	type action struct {
		desc string
		run  func() error
	}
	var plan []action
	add := func(desc string, run func() error) { plan = append(plan, action{desc, run}) }

	if _, err := os.Stat(vaultPath); os.IsNotExist(err) {
		add("create "+vaultPath, func() error { return os.MkdirAll(vaultPath, 0o755) })
	}
	if !adopt {
		for _, dir := range prof.Scaffold {
			target := filepath.Join(vaultPath, filepath.FromSlash(dir))
			if _, err := os.Stat(target); os.IsNotExist(err) {
				add("create folder "+dir, func() error { return os.MkdirAll(target, 0o755) })
			}
		}
		for _, rf := range prof.RootFiles {
			target := filepath.Join(vaultPath, rf)
			if _, err := os.Stat(target); err == nil {
				continue
			}
			content, err := prof.RootFile(rf)
			if err != nil {
				return fmt.Errorf("profile %s: root file %s: %w", prof.Name, rf, err)
			}
			add("create "+rf, func() error { return os.WriteFile(target, content, 0o644) })
		}
	}
	gi := filepath.Join(vaultPath, ".gitignore")
	if _, err := os.Stat(gi); os.IsNotExist(err) {
		add("create .gitignore", func() error { return os.WriteFile(gi, []byte(defaultGitignore), 0o644) })
	}

	g := gitx.Git{Dir: vaultPath}
	needRepo := true
	if _, err := os.Stat(vaultPath); err == nil {
		needRepo = !g.IsRepo()
	}
	if needRepo {
		add("git init", func() error { return gitx.Init(vaultPath) })
	}
	add("initial snapshot commit (if anything changed)", func() error {
		if clean, err := g.IsClean(); err != nil || clean {
			return err
		}
		if err := g.AddAll(); err != nil {
			return err
		}
		_, err := g.Commit("pkms init")
		return err
	})
	if registered == nil {
		add(fmt.Sprintf("register vault %q in %s", name, cfg.Path), func() error {
			return config.AppendVault(cfg.Path, config.Vault{Name: name, Path: vaultPath, Profile: profileName})
		})
	}

	if dryRun {
		fmt.Fprintf(out, "pkms init plan for %s (profile %s):\n", vaultPath, prof.Name)
		for _, a := range plan {
			fmt.Fprintf(out, "  - %s\n", a.desc)
		}
		return nil
	}
	for _, a := range plan {
		if err := a.run(); err != nil {
			return fmt.Errorf("%s: %w", a.desc, err)
		}
	}
	fmt.Fprintf(out, "vault %q ready at %s (profile %s)\n", name, vaultPath, prof.Name)
	fmt.Fprintf(out, "next: `pkms doctor` to verify, `pkms snapshot` to take a snapshot\n")
	return nil
}
