package usinasrc

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// DispatchRequest is one unit of work handed to the usina.
//
// Repo is owner/name and nothing else: the usina resolves the canonical
// checkout, the worktree and the branch from it, so hvb never passes a path
// and can never disagree with the usina about where the work happens.
type DispatchRequest struct {
	Repo       string // owner/name; usina resolves the canonical checkout from it
	Unidade    string
	Issue      int // 0 omits --issue
	PromptFile string
	DryRun     bool
}

// DispatchResult is the JSON object the usina prints on stdout. Unlisted keys
// are ignored, the same stance decodeCards takes, so the usina growing its
// payload cannot break this parse.
//
// Issue is a string because that is what the usina writes: measured
// 2026-09-17, the `.usina-despacho.json` of a dispatch without an issue
// carries `"issue": ""` rather than a number or null.
type DispatchResult struct {
	Unidade   string `json:"unidade"`
	Repo      string `json:"repo"`
	Issue     string `json:"issue"`
	Worktree  string `json:"worktree"`
	Branch    string `json:"branch"`
	Workspace string `json:"workspace"`
	Pane      string `json:"pane"`
	Outcome   string `json:"outcome"`
	DryRun    bool   `json:"dry_run"`
}

// Dispatch asks the usina to run one unit of work and returns what it printed.
//
// The argv is MEASURED, not remembered (AGENTS.md rule 3). Measured 2026-09-17
// against the installed usina: `usina agente dispatch` has no --help and
// answers "flags aceitas: --dry-run, --issue, --nivel, --prompt-file, --repo,
// --unidade". Two consequences are wired in here:
//
//   - --prompt-file is mandatory outside a dry-run, because the usina itself
//     refuses inline specs ("spec em arquivo, nunca inline"). Refusing here
//     instead of letting the child refuse keeps the fix in hvb's own message.
//   - --nivel only travels with --issue, and this caller never sends it: the
//     level is the usina's own routing decision and hvb has no measurement to
//     override it with.
//
// The order of the flags is pinned by TestDispatchArgvWithIssue and
// TestDispatchArgvWithoutIssue, so a reordering here is a test failure rather
// than a silent change to what the owner reads in a process list.
func Dispatch(run Runner, req DispatchRequest) (DispatchResult, error) {
	repo := strings.TrimSpace(req.Repo)
	unidade := strings.TrimSpace(req.Unidade)
	promptFile := strings.TrimSpace(req.PromptFile)

	if run == nil {
		return DispatchResult{}, errors.New("usinasrc: nenhum Runner injetado")
	}
	if owner, name, ok := strings.Cut(repo, "/"); !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return DispatchResult{}, fmt.Errorf(
			"usinasrc: repo %q nao e owner/name: informe os dois segmentos, como aryrabelo/ceo-bora", req.Repo)
	}
	if unidade == "" {
		return DispatchResult{}, errors.New(
			"usinasrc: unidade vazia: derive uma com Unidade(titulo, numero) antes de despachar")
	}
	if promptFile == "" && !req.DryRun {
		return DispatchResult{}, errors.New(
			"usinasrc: --prompt-file vazio: a usina exige a spec em arquivo, nunca inline " +
				"(grave o prompt e passe o caminho, ou use DryRun para conferir o argv sem arquivo)")
	}

	args := []string{"agente", "dispatch", "--repo", repo, "--unidade", unidade}
	// A non-positive issue is absence, not issue 0: the flag is omitted rather
	// than sent with a number the usina would have to reject.
	if req.Issue > 0 {
		args = append(args, "--issue", strconv.Itoa(req.Issue))
	}
	if promptFile != "" {
		args = append(args, "--prompt-file", promptFile)
	}
	if req.DryRun {
		args = append(args, "--dry-run")
	}

	stdout, err := run(SourceUsina, args...)
	if err != nil {
		return DispatchResult{}, err
	}
	return decodeDispatch(stdout)
}

// decodeDispatch reads the usina's answer, naming the usina in every error the
// way decodeCards names its own source — the caller has two binaries to blame
// and the message has to say which one lied.
//
// A missing `unidade` costs the answer instead of returning a zero value that
// reads as a successful dispatch nobody can track: the usina prints the unit
// it dispatched on every path, so its absence means the object on stdout is
// not a dispatch at all.
func decodeDispatch(stdout []byte) (DispatchResult, error) {
	var result DispatchResult
	if err := json.Unmarshal(stdout, &result); err != nil {
		return DispatchResult{}, fmt.Errorf("saida de %s nao e um objeto JSON de despacho: %w: %s",
			SourceUsina, err, excerpt(stdout))
	}
	if strings.TrimSpace(result.Unidade) == "" {
		return DispatchResult{}, fmt.Errorf("%s: despacho sem 'unidade' na saida: %s",
			SourceUsina, excerpt(stdout))
	}
	return result, nil
}

// excerpt bounds a child's stdout the same way runError bounds its stderr, for
// the same reason: the text is useful in an error the board renders, all of it
// is not.
func excerpt(stdout []byte) string {
	text := strings.TrimSpace(string(stdout))
	if text == "" {
		return "(vazio)"
	}
	if len(text) > maxStderrBytes {
		return text[:maxStderrBytes] + "…"
	}
	return text
}

// Unidade is the worktree/branch slug for an issue card.
//
// This is how the owner already names worktrees, measured rather than invented:
// `.usina-despacho.json` of this very worktree carries
// `"unidade": "linha-de-producao"` for the issue titled "linha de produção do
// hvb" — lowercase, accents reduced to ASCII, spaces collapsed to single
// hyphens. So the slug is derived from the title instead of asking the owner to
// type one, and a title that reduces to nothing falls back to issue-<number>,
// which is still a name a worktree can carry.
//
// The number is part of the name rather than decoration on it. The usina
// derives BOTH the checkout and the branch from the unit (measured in
// usina/verbos/agente.py: `worktrees/<repo>/<unidade>` and `agente/<unidade>`),
// so two cards titled the same way — or titled differently only past the cut —
// would dispatch the second agent into the first card's worktree. The number
// takes its bytes out of the title's budget rather than out of the limit, so
// the name stays as short as it was.
func Unidade(title string, number int) string {
	// A non-positive number is absence, not issue 0: there is nothing to
	// disambiguate with, and a "-0" tail would name a card that cannot exist.
	suffix := ""
	if number > 0 {
		suffix = "-" + strconv.Itoa(number)
	}
	if slug := cutAtWord(asciiSlug(title), unidadeMax-len(suffix)); slug != "" {
		return slug + suffix
	}
	return "issue-" + strconv.Itoa(number)
}

// unidadeMax is how many characters of the whole unit name survive — the title
// and the issue number together. A unit name becomes a directory name and a
// branch name, so it is kept short enough to read in a `git branch` listing
// next to the `agente/` prefix.
const unidadeMax = 48

// asciiSlug reduces a title to lowercase ASCII words joined by single hyphens.
//
// Building the separators as it goes — a hyphen is written only before a kept
// character, and only when something was already written — is what collapses
// runs and trims both ends without a second pass over the string.
func asciiSlug(title string) string {
	var slug strings.Builder
	slug.Grow(len(title))

	pendingSeparator := false
	for _, r := range title {
		r = unicode.ToLower(r)
		if folded, ok := asciiFold[r]; ok {
			r = folded
		}
		switch {
		case r >= combiningFirst && r <= combiningLast:
			// A dropped diacritic must not separate words: "produção" written
			// decomposed is "produc" + U+0327 + "ao", one word, not two.
			continue
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			if pendingSeparator && slug.Len() > 0 {
				slug.WriteByte('-')
			}
			pendingSeparator = false
			slug.WriteRune(r)
		default:
			pendingSeparator = true
		}
	}
	return slug.String()
}

// cutAtWord shortens a slug to max bytes without splitting a word. Byte
// indexing is safe because asciiSlug produced the string and it is ASCII.
//
// A single word longer than the limit has no boundary to back off to, and is
// cut hard: degrade rather than refuse (AGENTS.md) — a truncated name still
// identifies the worktree, while falling back to issue-<N> would throw away a
// title the owner can read.
//
// A max the issue number has already eaten leaves no room for a title, and the
// empty answer is what sends Unidade to its issue-<N> name rather than to a
// slice of a string that is shorter than the index.
func cutAtWord(slug string, max int) string {
	if max <= 0 {
		return ""
	}
	if len(slug) <= max {
		return slug
	}
	head := slug[:max]
	if slug[max] == '-' {
		return head // the cut already landed on a word boundary
	}
	if boundary := strings.LastIndexByte(head, '-'); boundary > 0 {
		return head[:boundary]
	}
	return head
}

// The combining diacritical marks block, dropped so a decomposed title folds
// the same as a precomposed one.
const (
	combiningFirst = 0x0300
	combiningLast  = 0x036F
)

// asciiFold reduces the accented letters the owner's titles actually contain.
//
// There is no dependency doing this: golang.org/x/text/unicode/norm would fold
// every script, and the four declared dependencies (AGENTS.md) do not include
// it for one slug function. The table is therefore Portuguese plus the Latin-1
// letters a Brazilian keyboard reaches, keyed by the lowercase rune because
// asciiSlug lowercases first. A letter with no entry here becomes a separator,
// which is why a title written entirely in Cyrillic or CJK falls back to
// issue-<N> instead of producing a slug nobody can type.
var asciiFold = map[rune]rune{
	'á': 'a', 'à': 'a', 'â': 'a', 'ã': 'a', 'ä': 'a', 'å': 'a',
	'ç': 'c',
	'é': 'e', 'è': 'e', 'ê': 'e', 'ë': 'e',
	'í': 'i', 'ì': 'i', 'î': 'i', 'ï': 'i',
	'ñ': 'n',
	'ó': 'o', 'ò': 'o', 'ô': 'o', 'õ': 'o', 'ö': 'o', 'ø': 'o',
	'ú': 'u', 'ù': 'u', 'û': 'u', 'ü': 'u',
	'ý': 'y', 'ÿ': 'y',
}
