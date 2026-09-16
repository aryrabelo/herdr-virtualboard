package cli

import (
	"github.com/spf13/cobra"
)

// Version is the hvb release, set by the linker at build time.
var Version = "dev"

// NewRoot builds the hvb command tree.
func NewRoot(app *App) *cobra.Command {
	root := &cobra.Command{
		Use:   "hvb",
		Short: "Kanban board and agent dispatcher for a VirtualBoard workspace, inside Herdr",
		Long: `hvb turns a VirtualBoard workspace into a kanban board and dispatches
VirtualBoard role agents into visible Herdr panes.

The board is the repository: vb and the markdown specs under
.virtualboard/features/ are the single source of truth. hvb records only which
pane is running which feature.`,
		SilenceUsage:      true,
		SilenceErrors:     true,
		DisableAutoGenTag: true,
	}

	flags := root.PersistentFlags()
	flags.StringVar(&app.RootFlag, "root", "", "project root holding .virtualboard (default: discovered from the working directory)")
	flags.BoolVar(&app.JSONFlag, "json", false, "emit machine-readable JSON")
	flags.StringVar(&app.Session, "session", "", "target a named Herdr session")

	root.AddCommand(
		newTUICommand(app),
		newFeatureCommand(app),
		newRunCommand(app),
		newRoleCommand(app),
		newHarnessCommand(app),
		newDoctorCommand(app),
		newSkillCommand(app),
		newVersionCommand(app),
	)
	return root
}
