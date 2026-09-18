package cli

import (
	"strings"

	"github.com/spf13/cobra"
	"github.com/virtualboard/herdr-virtualboard/internal/colunas"
	"github.com/virtualboard/herdr-virtualboard/internal/runs"
)

// newStateCommand exposes the column store to whatever else reads the queue.
//
// It exists because decision C of ceo-bora#321 puts six columns in hvb's own
// state and hvb writes nothing to GitHub: without a way to read that state
// back, the column would be visible only to the person looking at the board.
// `kit.py fronteira`/`rota` ask this instead, and hvb still writes nowhere.
func newStateCommand(app *App) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "state",
		Short: "Read the board columns hvb remembers",
	}
	cmd.AddCommand(newStateShowCommand(app))
	return cmd
}

// stateEntry is one row of `hvb state show`.
//
// It is the store's own Entry re-declared rather than emitted directly, because
// this is a published contract and the store's shape is not: adding a field to
// colunas.Entry should not silently change what a script downstream parses.
type stateEntry struct {
	ID     string `json:"id"`
	Repo   string `json:"repo,omitempty"`
	Issue  int    `json:"issue,omitempty"`
	Column string `json:"column"`
	SetAt  string `json:"set_at,omitempty"`
	SetBy  string `json:"set_by,omitempty"`
}

func newStateShowCommand(app *App) *cobra.Command {
	var repo string
	cmd := &cobra.Command{
		Use:   "show",
		Short: "Print the stored column of every card of a repository",
		Long: `Print the board column hvb remembers for each card of one repository.

The line's twelve columns have two different homes. Six of them are facts the
forge can prove — a pull request is open, merged or closed, an issue is closed —
and hvb reads those from GitHub every refresh and never writes them anywhere.
The other six (triage, planning, first-review, fr-approved, ready-to-review,
ready-to-merge) have no GitHub field at all, so hvb remembers them itself.

This command prints exactly what hvb remembers: one JSON array on stdout, the
same contract as the usina's own listing verbs. A card nobody has moved has no
remembered column and no row here — its column is whatever the forge and the
source say, which the caller can read from GitHub directly. Nothing here
contacts GitHub, so it is cheap enough to call in a loop.

--repo is the repository the cards belong to, which for a queue board is the
value of its --issues flag.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			repo = strings.TrimSpace(repo)
			if repo == "" {
				return Usage("--repo is required: name the repository whose cards to read, as owner/name")
			}
			// Both halves, not just the slash: `--repo /bugtoprompt`
			// and `--repo aryrabelo/` name no repository, and the
			// store would happily open a file keyed on the typo and
			// print an empty array — which reads as "nothing has
			// been moved" rather than "that is not a repository".
			owner, name, found := strings.Cut(repo, "/")
			if !found || strings.Contains(name, "/") ||
				strings.TrimSpace(owner) == "" || strings.TrimSpace(name) == "" {
				return Usage("--repo %s is not in owner/name form: both halves are required, e.g. aryrabelo/bugtoprompt", repo)
			}
			dir, err := runs.DataDir()
			if err != nil {
				return err
			}
			// Keyed on the repository, not on the whole flag set of the
			// board that wrote it: this command is reached without a
			// board, so a key derived from --vault and --charters would
			// be unreachable from here. The card IS the issue
			// (ceo-bora#321), so its repository is the natural key.
			store, err := colunas.Open(dir, colunas.BoardID(repo))
			if err != nil {
				return err
			}
			stored, err := store.List()
			if err != nil {
				return err
			}
			// Never nil: a caller parsing stdout unconditionally must
			// get `[]` for an empty store, not `null`.
			entries := make([]stateEntry, 0, len(stored))
			for _, entry := range stored {
				row := stateEntry{
					ID:     entry.ID,
					Repo:   entry.Repo,
					Issue:  entry.Issue,
					Column: entry.Column,
					SetBy:  entry.SetBy,
				}
				if !entry.SetAt.IsZero() {
					row.SetAt = entry.SetAt.UTC().Format(timeFormat)
				}
				entries = append(entries, row)
			}
			if app.Emit(entries) {
				return nil
			}
			if len(entries) == 0 {
				app.Print("no remembered column for %s (%s)", repo, store.Path())
				return nil
			}
			for _, entry := range entries {
				app.Print("%-24s %-18s %s", entry.ID, entry.Column, entry.SetBy)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&repo, "repo", "", "repository whose cards to read, as owner/name")
	return cmd
}

// timeFormat is RFC3339 with no sub-second noise, the same spelling the sources
// put in a card's frontmatter.
const timeFormat = "2006-01-02T15:04:05Z07:00"
