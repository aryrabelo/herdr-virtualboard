package tui

import (
	"context"
	"fmt"
	"strings"

	"github.com/virtualboard/herdr-virtualboard/internal/config"
	"github.com/virtualboard/herdr-virtualboard/internal/feature"
	"github.com/virtualboard/herdr-virtualboard/internal/roles"
)

// Handle applies one key press. It returns whether the board needs redrawing,
// which is every case except an ignored key.
func (m *Model) Handle(ctx context.Context, key Key) bool {
	if key.Name == KeyCtrlC {
		m.quitting = true
		return true
	}
	switch m.view {
	case ViewNewFeature:
		return m.handleForm(ctx, key)
	case ViewMovePicker, ViewDispatchPicker:
		return m.handlePicker(ctx, key)
	case ViewConfirm:
		return m.handleConfirm(ctx, key)
	case ViewDetail:
		return m.handleDetail(ctx, key)
	case ViewHelp:
		if key.Name == KeyEsc || key.Rune == 'q' || key.Rune == '?' {
			m.view = ViewBoard
		}
		return true
	default:
		return m.handleBoard(ctx, key)
	}
}

func (m *Model) handleBoard(ctx context.Context, key Key) bool {
	switch {
	case key.Name == KeyLeft, key.Rune == 'h':
		m.moveColumn(-1)
	case key.Name == KeyRight, key.Rune == 'l':
		m.moveColumn(1)
	case key.Name == KeyUp, key.Rune == 'k':
		m.moveCard(-1)
	case key.Name == KeyDown, key.Rune == 'j':
		m.moveCard(1)
	case key.Name == KeyHome:
		m.card[m.FocusedStatus()] = 0
	case key.Name == KeyEnd:
		m.card[m.FocusedStatus()] = len(m.Cards(m.FocusedStatus())) - 1
		m.clampSelection()
	case key.Name == KeyEnter:
		if m.FocusedCard() != nil {
			m.view, m.detailScroll = ViewDetail, 0
		}
	case key.Rune == '?':
		m.view = ViewHelp
	case key.Rune == 'q', key.Name == KeyEsc:
		m.quitting = true
	case key.Rune == 'r':
		m.Reload(ctx)
		m.setStatus("Refreshed.")
	case key.Name == KeyCtrlL:
		m.Reload(ctx)
	case key.Rune == 'n':
		m.openNewFeature()
	case key.Rune == 'm':
		m.openMovePicker()
	case key.Rune == 'd':
		m.openDispatchPicker()
	case key.Rune == 'H':
		m.shiftFocused(ctx, -1)
	case key.Rune == 'L':
		m.shiftFocused(ctx, 1)
	case key.Rune == 'e':
		m.openPriorityPicker()
	case key.Rune == 'o':
		m.focusRun(ctx)
	case key.Rune == 'x':
		m.confirmCancel()
	default:
		return false
	}
	return true
}

func (m *Model) handleDetail(ctx context.Context, key Key) bool {
	switch {
	case key.Name == KeyEsc, key.Rune == 'q':
		m.view = ViewBoard
	case key.Name == KeyUp, key.Rune == 'k':
		m.detailScroll--
	case key.Name == KeyDown, key.Rune == 'j':
		m.detailScroll++
	case key.Name == KeyPageUp:
		m.detailScroll -= m.height / 2
	case key.Name == KeyPageDown:
		m.detailScroll += m.height / 2
	case key.Name == KeyHome:
		m.detailScroll = 0
	case key.Rune == 'd':
		m.openDispatchPicker()
	case key.Rune == 'm':
		m.openMovePicker()
	case key.Rune == 'e':
		m.openPriorityPicker()
	case key.Rune == 'o':
		m.focusRun(ctx)
	case key.Rune == 'x':
		m.confirmCancel()
	case key.Rune == 'r':
		m.Reload(ctx)
	case key.Rune == '?':
		m.view = ViewHelp
	default:
		return false
	}
	if m.detailScroll < 0 {
		m.detailScroll = 0
	}
	return true
}

func (m *Model) handlePicker(ctx context.Context, key Key) bool {
	if m.picker == nil {
		m.view = ViewBoard
		return true
	}
	switch {
	case key.Name == KeyEsc, key.Rune == 'q':
		m.picker, m.view = nil, m.returnView()
	case key.Name == KeyUp, key.Rune == 'k':
		m.picker.move(-1)
	case key.Name == KeyDown, key.Rune == 'j':
		m.picker.move(1)
	case key.Name == KeyEnter:
		chosen, ok := m.picker.current()
		if !ok || chosen.Disabled {
			return true
		}
		onChoose := m.picker.onChoose
		m.picker, m.view = nil, m.returnView()
		if err := onChoose(chosen.Value); err != nil {
			m.setError(err)
		}
		m.Reload(ctx)
	default:
		return false
	}
	return true
}

func (m *Model) handleConfirm(ctx context.Context, key Key) bool {
	if m.confirm == nil {
		m.view = ViewBoard
		return true
	}
	switch {
	case key.Rune == 'y', key.Rune == 'Y':
		onYes := m.confirm.onYes
		m.confirm, m.view = nil, m.returnView()
		if err := onYes(); err != nil {
			m.setError(err)
		}
		m.Reload(ctx)
	case key.Name == KeyEsc, key.Rune == 'n', key.Rune == 'q':
		m.confirm, m.view = nil, m.returnView()
	default:
		return false
	}
	return true
}

func (m *Model) handleForm(ctx context.Context, key Key) bool {
	if m.form == nil {
		m.view = ViewBoard
		return true
	}
	switch {
	case key.Name == KeyEsc:
		m.form, m.view = nil, ViewBoard
	case key.Name == KeyTab, key.Name == KeyDown:
		m.form.moveField(1)
	case key.Name == KeyShiftTab, key.Name == KeyUp:
		m.form.moveField(-1)
	case key.Name == KeyLeft:
		m.form.cycle(-1)
	case key.Name == KeyRight:
		m.form.cycle(1)
	case key.Name == KeyBackspace:
		m.form.backspace()
	case key.Name == KeyEnter:
		values := m.form.values()
		if err := m.form.onSubmit(values); err != nil {
			m.form.err = err.Error()
			return true
		}
		m.form, m.view = nil, ViewBoard
		m.Reload(ctx)
	case key.Rune != 0:
		m.form.typeRune(key.Rune)
	default:
		return false
	}
	return true
}

// returnView is where an overlay goes back to. Detail stays detail: an overlay
// opened from a card's detail should not eject the user to the board.
func (m *Model) returnView() View {
	if m.detailScroll > 0 || m.view == ViewDetail {
		return ViewDetail
	}
	return ViewBoard
}

func (m *Model) moveColumn(delta int) {
	m.column += delta
	if m.column < 0 {
		m.column = 0
	}
	if order := m.columnOrder(); m.column >= len(order) {
		m.column = len(order) - 1
	}
}

func (m *Model) moveCard(delta int) {
	status := m.FocusedStatus()
	count := len(m.Cards(status))
	if count == 0 {
		return
	}
	index := m.card[status] + delta
	if index < 0 {
		index = 0
	}
	if index >= count {
		index = count - 1
	}
	m.card[status] = index
}

// openMovePicker offers the legal destinations for the focused feature, with
// the illegal ones shown and greyed out so the lifecycle is visible rather than
// merely enforced.
func (m *Model) openMovePicker() {
	card := m.FocusedCard()
	if card == nil {
		m.setStatus("No card selected.")
		return
	}
	current := card.Spec.Status
	var options []option
	for _, status := range feature.Statuses {
		if status == current {
			continue
		}
		entry := option{Value: string(status), Label: string(status)}
		if !feature.CanTransition(current, status) {
			entry.Disabled = true
			entry.Reason = fmt.Sprintf("no %s → %s transition", current, status)
		} else if column := m.backend.Config().Column(status); column.Auto {
			entry.Description = "dispatches an agent automatically"
		}
		options = append(options, entry)
	}
	p := &picker{
		title:    fmt.Sprintf("Move %s", card.Spec.ID),
		subtitle: fmt.Sprintf("%s — currently %s", card.Spec.Title, current),
		options:  options,
		onChoose: func(value string) error {
			target, ok := feature.ParseStatus(value)
			if !ok {
				return fmt.Errorf("unknown status %q", value)
			}
			owner := ""
			if target == feature.InProgress {
				owner = m.backend.Owner()
			} else if target == feature.Done || target == feature.Backlog {
				owner = feature.Unassigned
			}
			if err := m.backend.Move(context.Background(), card.Spec.ID, target, owner); err != nil {
				return err
			}
			m.setStatus("%s → %s", card.Spec.ID, target)
			m.offerDispatch(card, target)
			return nil
		},
	}
	if entry, ok := p.current(); ok && entry.Disabled {
		p.move(1)
	}
	m.picker, m.view = p, ViewMovePicker
}

// shiftFocused moves the focused feature one column left or right, when the
// lifecycle allows it.
func (m *Model) shiftFocused(ctx context.Context, delta int) {
	card := m.FocusedCard()
	if card == nil {
		return
	}
	current := card.Spec.Status
	index := -1
	for position, status := range feature.Statuses {
		if status == current {
			index = position
			break
		}
	}
	target := index + delta
	if index < 0 || target < 0 || target >= len(feature.Statuses) {
		return
	}
	status := feature.Statuses[target]
	if !feature.CanTransition(current, status) {
		m.setError(fmt.Errorf("VirtualBoard does not allow %s → %s (try: %s)", current, status,
			statusList(current.NextStatuses())))
		return
	}
	owner := ""
	if status == feature.InProgress {
		owner = m.backend.Owner()
	}
	if err := m.backend.Move(ctx, card.Spec.ID, status, owner); err != nil {
		m.setError(err)
		return
	}
	m.setStatus("%s → %s", card.Spec.ID, status)
	m.Reload(ctx)
	m.focusFeature(card.Spec.ID)
	m.offerDispatch(card, status)
}

// offerDispatch asks whether to start an agent on a feature that has just
// entered in-progress.
//
// Starting work is exactly when an agent is useful, and it is also exactly when
// the user has the context to decide how isolated it should be — so the board
// asks once, here, rather than making worktrees a setting somebody has to find.
// Declining is the first option and one keystroke away, because most moves are
// a human picking up the work themselves.
func (m *Model) offerDispatch(card *Card, status feature.Status) {
	if status != feature.InProgress || card == nil {
		return
	}
	if card.Active != nil {
		return
	}
	if len(m.backend.Roles()) == 0 {
		return
	}
	cfg := m.backend.Config()
	options := []option{
		{Value: dispatchNone, Label: "No — I will work on it", Description: "just move the card"},
		{Value: dispatchHere, Label: "Agent, in the project", Description: "works directly in the working tree"},
		{Value: dispatchWorktree, Label: "Agent, in a worktree", Description: "isolated checkout on the feature's own branch"},
		{Value: dispatchPR, Label: "Agent, worktree + pull request", Description: "pushes the branch and opens a PR on success"},
	}
	m.picker = &picker{
		title:    fmt.Sprintf("%s is now in progress", card.Spec.ID),
		subtitle: fmt.Sprintf("%s — start an agent on it?", card.Spec.Title),
		options:  options,
		index:    defaultDispatchChoice(cfg),
		onChoose: func(value string) error {
			if value == dispatchNone {
				return nil
			}
			yes, no := true, false
			worktree, wantPR := &no, &no
			switch value {
			case dispatchWorktree:
				worktree = &yes
			case dispatchPR:
				worktree, wantPR = &yes, &yes
			}
			run, err := m.backend.DispatchWith(context.Background(), card.Spec, "", "", worktree, wantPR)
			if err != nil {
				return err
			}
			if run.Worktree != nil {
				m.setStatus("Dispatched %s as %s on %s", run.FeatureID, run.Role, run.Worktree.Branch)
				return nil
			}
			m.setStatus("Dispatched %s as %s in %s", run.FeatureID, run.Role, run.PaneID)
			return nil
		},
	}
	m.view = ViewDispatchPicker
}

// The values offerDispatch's picker returns.
const (
	dispatchNone     = "none"
	dispatchHere     = "here"
	dispatchWorktree = "worktree"
	dispatchPR       = "pr"
)

// defaultDispatchChoice preselects the row matching configuration, so a board
// set up for worktrees opens on the worktree option — but never on a row that
// starts an agent unasked. Declining stays the default when nothing is
// configured.
func defaultDispatchChoice(cfg *config.Config) int {
	switch {
	case cfg.Forge.Enabled && cfg.Worktree.Enabled:
		return 3
	case cfg.Worktree.Enabled:
		return 2
	default:
		return 0
	}
}

// openDispatchPicker asks which role to dispatch, pre-selecting the one the
// feature's labels and status suggest.
func (m *Model) openDispatchPicker() {
	card := m.FocusedCard()
	if card == nil {
		m.setStatus("No card selected.")
		return
	}
	if card.Active != nil {
		m.setError(fmt.Errorf("%s already has a run in progress (press x to cancel it)", card.Spec.ID))
		return
	}
	available := m.backend.Roles()
	if len(available) == 0 {
		m.setError(fmt.Errorf("no agent charters in .virtualboard/agents — cannot pick a role"))
		return
	}
	suggested, _ := roles.Suggest(available, card.Spec, m.backend.Config().Role)

	options := make([]option, 0, len(available))
	selected := 0
	for index, role := range available {
		options = append(options, option{Value: role.Key, Label: role.Key, Description: role.Description})
		if role.Key == suggested.Key {
			selected = index
		}
	}
	kind := m.backend.Config().Harness
	if column := m.backend.Config().Column(card.Spec.Status); column.Harness != "" {
		kind = column.Harness
	}
	m.picker = &picker{
		title:    fmt.Sprintf("Dispatch %s with %s", card.Spec.ID, kind),
		subtitle: card.Spec.Title,
		options:  options,
		index:    selected,
		onChoose: func(value string) error {
			run, err := m.backend.Dispatch(context.Background(), card.Spec, value, "", false)
			if err != nil {
				return err
			}
			if run.Worktree != nil {
				m.setStatus("Dispatched %s as %s on %s", run.FeatureID, run.Role, run.Worktree.Branch)
				return nil
			}
			m.setStatus("Dispatched %s as %s in %s", run.FeatureID, run.Role, run.PaneID)
			return nil
		},
	}
	m.view = ViewDispatchPicker
}

func (m *Model) openPriorityPicker() {
	card := m.FocusedCard()
	if card == nil {
		return
	}
	options := []option{
		{Value: "P0", Label: "P0", Description: "drop everything"},
		{Value: "P1", Label: "P1", Description: "this sprint"},
		{Value: "P2", Label: "P2", Description: "normal"},
		{Value: "P3", Label: "P3", Description: "someday"},
	}
	selected := 2
	for index, entry := range options {
		if entry.Value == card.Spec.Priority {
			selected = index
		}
	}
	m.picker = &picker{
		title:    fmt.Sprintf("Priority for %s", card.Spec.ID),
		subtitle: card.Spec.Title,
		options:  options,
		index:    selected,
		onChoose: func(value string) error {
			if err := m.backend.SetField(context.Background(), card.Spec.ID, "priority", value); err != nil {
				return err
			}
			m.setStatus("%s priority %s", card.Spec.ID, value)
			return nil
		},
	}
	m.view = ViewMovePicker
}

func (m *Model) openNewFeature() {
	m.form = &form{
		title: "New feature",
		fields: []field{
			{Key: "title", Label: "title", Help: "what the feature delivers, in a line"},
			{Key: "labels", Label: "labels", Help: "space-separated, kebab-case; they steer the role"},
			{Key: "priority", Label: "priority", Choices: []string{"P2", "P0", "P1", "P3"}, Value: "P2"},
		},
		onSubmit: func(values map[string]string) error {
			title := strings.TrimSpace(values["title"])
			if title == "" {
				return fmt.Errorf("a title is required")
			}
			labels := strings.Fields(values["labels"])
			id, err := m.backend.Create(context.Background(), title, labels, values["priority"])
			if err != nil {
				return err
			}
			m.setStatus("Created %s", id)
			return nil
		},
	}
	m.view = ViewNewFeature
}

func (m *Model) focusRun(ctx context.Context) {
	card := m.FocusedCard()
	if card == nil {
		return
	}
	run := card.Active
	if run == nil {
		run = latestRun(card.Runs)
	}
	if run == nil || run.PaneID == "" {
		m.setStatus("No pane to focus for %s.", card.Spec.ID)
		return
	}
	if err := m.backend.Focus(ctx, run.ID); err != nil {
		m.setError(err)
		return
	}
	m.setStatus("Focused %s", run.PaneID)
}

func (m *Model) confirmCancel() {
	card := m.FocusedCard()
	if card == nil || card.Active == nil {
		m.setStatus("No active run on this card.")
		return
	}
	run := card.Active
	m.confirm = &confirmation{
		title: "Cancel run",
		body: fmt.Sprintf("Stop the %s agent working %s and close its pane? Anything it has not committed is lost.",
			run.Role, run.FeatureID),
		onYes: func() error {
			if err := m.backend.Cancel(context.Background(), run.ID); err != nil {
				return err
			}
			m.setStatus("Cancelled the run on %s", run.FeatureID)
			return nil
		},
	}
	m.view = ViewConfirm
}

func statusList(statuses []feature.Status) string {
	if len(statuses) == 0 {
		return "nothing; it is terminal"
	}
	out := make([]string, len(statuses))
	for index, status := range statuses {
		out[index] = string(status)
	}
	return strings.Join(out, ", ")
}
