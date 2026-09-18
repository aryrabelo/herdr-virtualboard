// Package colunas stores the board column a card was put in.
//
// This is deliberately a SECOND store, standing next to internal/runs instead
// of widening it, and the split is the whole point. Rule 5 of AGENTS.md —
// "never widen what hvb stores" — keeps the run store holding exactly one
// thing: which Herdr pane is running which feature. Decision C of ceo-bora#321
// then declares twelve columns, six of which the forge has no fact for
// (triage, planning, first-review, fr-approved, ready-to-review,
// ready-to-merge): no GitHub field answers "is this issue triaged", so hvb has
// to remember that answer itself.
//
// That is the tension: hvb must not shadow a fact it can read, and it must
// remember a decision nobody else records. Adding a column field to
// internal/runs would have settled it by breaking rule 5 for every reader of
// the run store, and by tying a card's column to the lifetime of a dispatch —
// a card can sit in Triage having never been dispatched at all. A separate file
// settles it by keeping the run store narrow and making the new state obvious:
// one file per board, holding only the cards someone moved by hand, deletable
// without losing a single run. Anything a source can measure belongs in an
// `hvb:` label instead, and internal/linha lets those labels beat this store on
// read, so a stale entry here can never outvote the repository.
//
// The on-disk mechanics are copied from internal/runs/store.go rather than
// invented: a versioned document, atomic replacement, a lock file, a file from
// the future discarded, a corrupt file starting clean. The lock is the one
// place this package deliberately does NOT copy it: internal/runs takes its
// lock by exclusive-create and reclaims one that looks old, which cannot tell
// a crashed writer from a slow one; lockFile here asks the kernel instead.
// internal/runs is not imported — Open takes the data directory — so the
// env-var chain ($HVB_DATA_DIR, $XDG_DATA_HOME, ~/.local/share) keeps exactly
// one definition and this package cannot drift out of step with it.
package colunas

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Entry is one card's stored column.
//
// Column is a plain string, not a feature.Status: the store must hold no
// opinion about the declared line, which configuration owns. A column renamed
// in `.hvb.toml` then leaves a harmless orphan entry that internal/linha
// ignores, instead of an entry this package would have to reject and a file
// that could no longer be read.
type Entry struct {
	ID     string    `json:"id"`
	Repo   string    `json:"repo,omitempty"`
	Issue  int       `json:"issue,omitempty"`
	Column string    `json:"column"`
	SetAt  time.Time `json:"set_at"`
	SetBy  string    `json:"set_by,omitempty"`
}

// document is the on-disk file shape.
type document struct {
	Version int     `json:"version"`
	Board   string  `json:"board"`
	Entries []Entry `json:"entries"`
}

// version is the column-file schema version. Bump it when the shape changes in
// a way an older hvb would misread; load discards a file from the future rather
// than guessing at it.
const version = 1

// Store is one board's column file. It is safe for concurrent use within one
// process and uses atomic replacement plus a lock file across processes.
type Store struct {
	path  string
	board string
	mu    sync.Mutex
}

// BoardID is the store key for a board, derived from what that board reads.
//
// Deterministic, so the same flags reopen the same store after a restart — that
// is the whole reason a card moved to Triage is still in Triage tomorrow.
// Order- and whitespace-insensitive because `--repo a --repo b` and
// `--repo b --repo a` are the same board to the human typing them, and a
// trailing space in a shell alias must not fork the file. A source that trims
// to nothing drops out: an unset flag names no source. Truncated to 16 hex
// digits, which is short enough to paste into a bug report and still far beyond
// collision range for the handful of boards one machine opens.
func BoardID(sources ...string) string {
	keys := make([]string, 0, len(sources))
	for _, source := range sources {
		if trimmed := strings.TrimSpace(source); trimmed != "" {
			keys = append(keys, trimmed)
		}
	}
	sort.Strings(keys)
	sum := sha256.Sum256([]byte(strings.Join(keys, "\n")))
	return "queue-" + hex.EncodeToString(sum[:])[:16]
}

// Open returns the column store at <dir>/columns/<boardID>.json, creating the
// directory if needed. The caller supplies dir so this package holds no second
// copy of the data-directory rules.
func Open(dir, boardID string) (*Store, error) {
	if strings.TrimSpace(dir) == "" {
		return nil, errors.New("column store needs a data directory (pass the same one the run store uses: runs.DataDir())")
	}
	if strings.TrimSpace(boardID) == "" {
		return nil, errors.New("column store needs a board id (build one from the board's sources with colunas.BoardID)")
	}
	columnsDir := filepath.Join(dir, "columns")
	if err := os.MkdirAll(columnsDir, 0o700); err != nil {
		return nil, fmt.Errorf("create column directory %s: %w", columnsDir, err)
	}
	return &Store{path: filepath.Join(columnsDir, boardID+".json"), board: boardID}, nil
}

// Path is the column file's location, for diagnostics.
func (s *Store) Path() string { return s.path }

// List returns every stored entry, ordered by ID so two reads of an unchanged
// store produce byte-identical output.
func (s *Store) List() ([]Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	doc, err := s.load()
	if err != nil {
		return nil, err
	}
	entries := append([]Entry(nil), doc.Entries...)
	sortEntries(entries)
	return entries, nil
}

// Columns returns card id -> stored column, which is the shape internal/linha
// consumes.
func (s *Store) Columns() (map[string]string, error) {
	entries, err := s.List()
	if err != nil {
		return nil, err
	}
	columns := make(map[string]string, len(entries))
	for _, entry := range entries {
		columns[entry.ID] = entry.Column
	}
	return columns, nil
}

// Set records a card's column, replacing any earlier entry for the same id.
//
// The replacement is wholesale rather than a field-by-field merge: the newest
// move is the whole truth about the card, and merging would let a stale Repo or
// Issue from a previous board survive underneath a fresh column.
func (s *Store) Set(entry Entry) error {
	if strings.TrimSpace(entry.ID) == "" {
		return errors.New("column store entry needs an id (the opaque card id its source minted)")
	}
	if strings.TrimSpace(entry.Column) == "" {
		return fmt.Errorf("column store entry %q needs a column; to forget a card's column call Clear(%q) instead", entry.ID, entry.ID)
	}
	if entry.SetAt.IsZero() {
		// An entry without a timestamp would make the card detail claim the
		// move happened in year 1, so stamp it here rather than leaving every
		// caller to remember. UTC because the file outlives the timezone of
		// the shell that wrote it.
		entry.SetAt = time.Now().UTC()
	}
	return s.mutate(func(doc *document) error {
		for i := range doc.Entries {
			if doc.Entries[i].ID == entry.ID {
				doc.Entries[i] = entry
				return nil
			}
		}
		doc.Entries = append(doc.Entries, entry)
		return nil
	})
}

// Clear forgets a card's column, letting its source decide again.
//
// An absent id is not an error: the caller wanted the card unstored and the
// card is unstored. Reporting it would make every cleanup path — a merged pull
// request, a card that left the board — branch on a difference that changes
// nothing.
func (s *Store) Clear(id string) error {
	return s.mutate(func(doc *document) error {
		kept := doc.Entries[:0]
		for _, entry := range doc.Entries {
			if entry.ID != id {
				kept = append(kept, entry)
			}
		}
		doc.Entries = kept
		return nil
	})
}

func (s *Store) mutate(apply func(*document) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	unlock, err := s.lockFile()
	if err != nil {
		return err
	}
	defer unlock()

	doc, err := s.load()
	if err != nil {
		return err
	}
	if err := apply(doc); err != nil {
		return err
	}
	return s.save(doc)
}

func (s *Store) load() (*document, error) {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return s.empty(), nil
		}
		return nil, fmt.Errorf("read column store %s: %w", s.path, err)
	}
	var doc document
	if err := json.Unmarshal(raw, &doc); err != nil {
		// A corrupt column file must not make the board refuse to open: a
		// stored column is a convenience on top of what the sources measure,
		// and the sources still measure it. Start clean, as the run store does.
		return s.empty(), nil
	}
	if doc.Version > version {
		// Written by a newer hvb, whose entries may mean something else.
		// Discarding is the honest read; guessing would silently move cards.
		return s.empty(), nil
	}
	doc.Version = version
	doc.Board = s.board
	return &doc, nil
}

func (s *Store) empty() *document {
	return &document{Version: version, Board: s.board}
}

func (s *Store) save(doc *document) error {
	// Sort before encoding too, not only in List: a stable file keeps a diff of
	// the user's data directory readable, and makes a repeated save a no-op.
	sortEntries(doc.Entries)
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("encode column store: %w", err)
	}
	raw = append(raw, '\n')
	temp, err := os.CreateTemp(filepath.Dir(s.path), ".columns-*.json")
	if err != nil {
		return fmt.Errorf("stage column store: %w", err)
	}
	name := temp.Name()
	defer os.Remove(name)
	if _, err := temp.Write(raw); err != nil {
		temp.Close()
		return fmt.Errorf("write column store: %w", err)
	}
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return fmt.Errorf("chmod column store: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close column store: %w", err)
	}
	if err := os.Rename(name, s.path); err != nil {
		return fmt.Errorf("replace column store: %w", err)
	}
	return nil
}

// lockFile takes a cross-process exclusive lock on the store through flock(2),
// retrying briefly. Every writer holds it for a single read-modify-write, so
// waiting is short.
//
// flock rather than the exclusive-create-plus-age heuristic internal/runs uses:
// an age check is a guess about whether the holder is still alive, and the
// guess loses a move when it is wrong. A writer descheduled past the staleness
// window — a laptop asleep, a machine under load, a debugger stopped at a
// breakpoint — would watch a second writer delete its live lock, both would
// read the same document, and whichever renamed last would publish a snapshot
// taken before the other's move. flock has no such window: the kernel holds
// the lock for exactly as long as the owning descriptor is open, so no lease
// can be reclaimed while its owner lives, and crash recovery — the only thing
// the age check bought — comes for free, because a dead process's descriptors
// are closed for it.
//
// The lock file is created and kept, never removed: unlinking it would let a
// waiter hold a lock on an unlinked inode while a third writer creates a fresh
// file and locks that instead, which is the same two-writers bug by another
// route. An empty `<board>.json.lock` left behind costs nothing and locks
// nobody out.
func (s *Store) lockFile() (func(), error) {
	path := s.path + ".lock"
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("lock column store: %w", err)
	}
	deadline := time.Now().Add(lockWait)
	for {
		err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			// The pid is a diagnostic, not the lock: it tells whoever
			// finds a board wedged which process to look at. Written
			// under the lock, so two writers cannot interleave it.
			if truncErr := file.Truncate(0); truncErr == nil {
				fmt.Fprintf(file, "%d\n", os.Getpid())
			}
			return func() {
				// Closing releases the lock too; unlocking first
				// keeps the release explicit rather than a side
				// effect a later edit could drop.
				_ = syscall.Flock(int(file.Fd()), syscall.LOCK_UN)
				file.Close()
			}, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) {
			file.Close()
			return nil, fmt.Errorf("lock column store %s: %w", path, err)
		}
		if time.Now().After(deadline) {
			file.Close()
			return nil, fmt.Errorf("lock column store: %s held for over %s by another hvb (`lsof %s` names it)", path, lockWait, path)
		}
		time.Sleep(lockPoll)
	}
}

const (
	lockWait = 5 * time.Second
	lockPoll = 20 * time.Millisecond
)

func sortEntries(entries []Entry) {
	sort.Slice(entries, func(i, j int) bool { return entries[i].ID < entries[j].ID })
}
