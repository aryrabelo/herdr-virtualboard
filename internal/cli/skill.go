package cli

import (
	"github.com/netors/herdr-virtualboard/internal/dispatch"
	"github.com/spf13/cobra"
)

func newSkillCommand(app *App) *cobra.Command {
	return &cobra.Command{
		Use:   "skill",
		Short: "Print the contract a dispatched agent is held to",
		Long: `Print the exact bytes hvb embeds in every dispatch prompt.

An agent can read the rules it is being judged against without taking hvb's
word for what they say, and a human can diff what changed between releases.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			app.Print("%s", dispatch.Skill)
			return nil
		},
	}
}
