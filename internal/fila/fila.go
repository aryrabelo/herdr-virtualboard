// Package fila reads the owner's queue ordering — the frontier — and decides
// which board column a card belongs to.
//
// Two halves, deliberately independent. LoadFrontier shells out to the ceo
// repository's kit.py to learn which issues are pickable right now and which
// are blocked; ColumnFor is pure policy over facts a card already carries.
// The board must render without a frontier, so a missing, broken, or empty
// kit.py is never fatal: LoadFrontier returns non-nil (indexable) maps plus a
// Reason naming who failed and how. Silent degradation is an amplifier, so
// nothing here returns an empty frontier without saying why.
//
// Absence is absence. An issue the frontier never mentioned does not appear in
// the map at all, and Priority.Has is the only way to ask. Rank 0 with
// Has true is a measured rank, never a sentinel.
package fila

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Runner invokes an external binary and returns its stdout. It is the single
// seam between this package and the outside world, so tests replace it with a
// fixture reader and never execute python3, kit.py, gh, or usina.
type Runner func(name string, args ...string) ([]byte, error)

// Priority is one issue's place in the frontier. Has distinguishes "the
// frontier ranked this issue first" (Rank 0, Has true) from "the frontier
// never mentioned it" (the zero value, which never reaches the map).
type Priority struct {
	Rank int
	Why  string
	Has  bool
}

// Frontier maps an issue number to its priority, plus the blocked set. Both
// maps are always non-nil: the board indexes them on every repaint and a nil
// map read is only safe by accident of Go, not by contract. Reason is empty
// when the frontier was measured, and names the failure when it was not.
type Frontier struct {
	Priority map[int]Priority
	Blocked  map[int]bool
	Reason   string
}

// kitItem is one entry of kit.py's `pronto` or `bloqueado` list. Both lists
// carry the same shape — `bloqueado` is an array of objects, not of integers —
// so one struct decodes both. `rank` is absent from `bloqueado`, which costs
// nothing: the blocked set only needs the number.
type kitItem struct {
	Titulo string `json:"titulo"`
	Numero int    `json:"numero"`
	Rank   int    `json:"rank"`
	Porque string `json:"porque"`
}

// kitFrontier mirrors the object `kit.py fronteira <slug>` prints on stdout.
// Unlisted keys are ignored, so kit.py growing its payload cannot break this.
type kitFrontier struct {
	Pronto    []kitItem `json:"pronto"`
	Bloqueado []kitItem `json:"bloqueado"`
}

// pythonBinary runs kit.py. kit.py is a script, not an executable on PATH, so
// the interpreter is named explicitly rather than relying on a shebang.
const pythonBinary = "python3"

// LoadFrontier asks kit.py for slug's frontier via run.
//
// A source that does not answer is reported, not hidden: kit.py missing, a
// non-zero exit, unparseable stdout, or an answer with neither list populated
// all yield empty-but-indexable maps and a Reason. An error is returned only
// for a mis-wired call — no runner, no kit path, no slug — where there is
// nothing to degrade from.
func LoadFrontier(run Runner, kitPath, slug string) (Frontier, error) {
	frontier := Frontier{
		Priority: make(map[int]Priority),
		Blocked:  make(map[int]bool),
	}

	kitPath = strings.TrimSpace(kitPath)
	slug = strings.TrimSpace(slug)
	switch {
	case run == nil:
		frontier.Reason = "fila: nenhum Runner injetado"
		return frontier, errors.New(frontier.Reason)
	case kitPath == "":
		frontier.Reason = "fila: caminho do kit.py vazio"
		return frontier, errors.New(frontier.Reason)
	case slug == "":
		frontier.Reason = "fila: slug do projeto vazio"
		return frontier, errors.New(frontier.Reason)
	}

	stdout, err := run(pythonBinary, kitPath, "fronteira", slug)
	if err != nil {
		frontier.Reason = fmt.Sprintf("fila: %s %s fronteira %s nao respondeu: %v",
			pythonBinary, kitPath, slug, err)
		return frontier, nil
	}

	var answer kitFrontier
	if err := json.Unmarshal(stdout, &answer); err != nil {
		frontier.Reason = fmt.Sprintf("fila: saida de %s %s fronteira %s nao e JSON: %v",
			pythonBinary, kitPath, slug, err)
		return frontier, nil
	}

	if len(answer.Pronto) == 0 && len(answer.Bloqueado) == 0 {
		frontier.Reason = fmt.Sprintf(
			"fila: kit.py respondeu sem pronto nem bloqueado para %s; colunas decididas so por label",
			slug)
		return frontier, nil
	}

	unnumbered := 0
	for _, item := range answer.Pronto {
		if item.Numero <= 0 {
			// A missing `numero` decodes to 0, and keying the map on it
			// would invent a phantom issue the board would then try to
			// rank. Dropped, and counted into Reason so the loss is visible.
			unnumbered++
			continue
		}
		frontier.Priority[item.Numero] = Priority{
			Rank: item.Rank,
			Why:  item.Porque,
			Has:  true,
		}
	}
	for _, item := range answer.Bloqueado {
		if item.Numero <= 0 {
			unnumbered++
			continue
		}
		frontier.Blocked[item.Numero] = true
	}
	if unnumbered > 0 {
		frontier.Reason = fmt.Sprintf("fila: %d item(ns) sem numero descartado(s) da fronteira de %s",
			unnumbered, slug)
	}

	return frontier, nil
}

// ExecRunner is the production Runner. Authentication and configuration are the
// child process's problem: nothing secret passes through here, and a nil Stdin
// guarantees kit.py cannot block this call waiting for input it will never get.
func ExecRunner(name string, args ...string) ([]byte, error) {
	return execRunnerWithTimeout(execTimeout, name, args...)
}

// execRunnerWithTimeout is ExecRunner with the deadline as an argument. The
// deadline is the one behaviour here that cannot be proven with a fixture, and
// no test may wait out the production value, so production passes execTimeout
// and tests pass milliseconds. Nothing else varies.
func execRunnerWithTimeout(timeout time.Duration, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	path, err := exec.LookPath(name)
	if err != nil {
		return nil, fmt.Errorf("%s nao esta no PATH: %w", name, err)
	}

	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Stdin = nil

	if err := cmd.Run(); err != nil {
		return nil, runError(name, timeout, ctx.Err(), err, stderr.Bytes())
	}
	return stdout.Bytes(), nil
}

// runError keeps the child's own diagnosis. kit.py prints its refusal to
// stderr ("fronteira: nao consegui ler as issues de …") and exits 1; that text
// is the useful part, the exit status alone is not.
//
// The deadline is the one failure where the child has no diagnosis to keep:
// the context kills it, so stderr is empty and ExitCode() is -1, and passing
// that through is what put "python3 saiu -1: signal: killed" on the board
// (measured) — a message naming neither the deadline nor its value. ctxErr is
// the only witness that the kill was ours, so it is read before the child's
// text; a child that exited on its own keeps its stderr and its exit code.
func runError(name string, timeout time.Duration, ctxErr, runErr error, stderr []byte) error {
	if errors.Is(ctxErr, context.DeadlineExceeded) {
		return fmt.Errorf("%s passou do prazo de %s e foi interrompido antes de responder", name, timeout)
	}

	reason := strings.TrimSpace(string(stderr))
	if reason == "" {
		reason = runErr.Error()
	}
	if len(reason) > maxStderrBytes {
		reason = reason[:maxStderrBytes] + "…"
	}

	var exitErr *exec.ExitError
	if errors.As(runErr, &exitErr) {
		return fmt.Errorf("%s saiu %d: %s", name, exitErr.ExitCode(), reason)
	}
	return fmt.Errorf("%s nao pode ser executado: %s", name, reason)
}

const (
	// execTimeout bounds one kit.py call. `fronteira` makes one gh request per
	// open issue, so its cost grows with the project: it answered in 69s on
	// the owner's real project (measured with `time`, one isolated successful
	// call, 33 open cards), and the previous 60s deadline sat below that and
	// killed it on every refresh. 3 minutes is ~2.6x the measurement, which
	// absorbs a project a couple of times larger or a slow gh round trip.
	// Still bounded on purpose: a board that hangs forever tells the owner
	// less than one that reports which deadline it blew.
	execTimeout = 3 * time.Minute
	// maxStderrBytes caps how much of the child's stderr reaches the board.
	maxStderrBytes = 512
)
