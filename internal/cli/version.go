package cli

import (
	"runtime"

	"github.com/spf13/cobra"
	"github.com/virtualboard/herdr-virtualboard/internal/herdrcli"
)

func newVersionCommand(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the hvb version and the Herdr build floor it requires",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			// The floor is reported as resolved rather than as compiled, so an
			// operator who overrode it in the environment sees the number the
			// gate will actually use.
			floor, err := herdrcli.ResolvedFloor()
			if err != nil {
				return err
			}
			info := map[string]any{
				"version":          Version,
				"go":               runtime.Version(),
				"platform":         runtime.GOOS + "/" + runtime.GOARCH,
				"minimum_herdr":    floor.Version,
				"minimum_protocol": floor.Protocol,
			}
			if app.Emit(info) {
				return nil
			}
			app.Print("hvb %s (%s, %s)", Version, runtime.Version(), info["platform"])
			app.Print("requires herdr %s / socket protocol %d or newer", floor.Version, floor.Protocol)
			return nil
		},
	}
}
