package cli

import (
	"runtime"

	"github.com/spf13/cobra"
	"github.com/virtualboard/herdr-virtualboard/internal/herdrcli"
)

func newVersionCommand(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the hvb version and the Herdr build it supports",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			info := map[string]any{
				"version":            Version,
				"go":                 runtime.Version(),
				"platform":           runtime.GOOS + "/" + runtime.GOARCH,
				"supported_herdr":    herdrcli.SupportedVersion,
				"supported_protocol": herdrcli.SupportedProtocol,
			}
			if app.Emit(info) {
				return nil
			}
			app.Print("hvb %s (%s, %s)", Version, runtime.Version(), info["platform"])
			app.Print("supports herdr %s / socket protocol %d", herdrcli.SupportedVersion, herdrcli.SupportedProtocol)
			return nil
		},
	}
}
