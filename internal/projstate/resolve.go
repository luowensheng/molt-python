package projstate

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// AmbiguousError is returned by Resolve when a query matches multiple
// projects. Callers should print Candidates and prompt for a more
// specific query (typically a hash prefix).
type AmbiguousError struct {
	Query      string
	Candidates []Entry
}

func (e *AmbiguousError) Error() string {
	names := make([]string, 0, len(e.Candidates))
	for _, c := range e.Candidates {
		names = append(names, fmt.Sprintf("%s [%s]", filepath.Base(c.ProjectDir), c.Hash))
	}
	return fmt.Sprintf("ambiguous: %d projects match %q (%s) — disambiguate by hash prefix",
		len(e.Candidates), e.Query, strings.Join(names, ", "))
}

// Resolve maps a user-supplied query to a single registered project.
// Query forms (tried in order):
//
//  1. Absolute path or "." — matches Entry.ProjectDir exactly (after
//     cleaning + EvalSymlinks). Useful when scripting from a known dir.
//  2. Hash prefix (≥4 hex chars, lower-case) — matches Entry.Hash by prefix.
//     Unique match wins; ambiguous returns AmbiguousError.
//  3. Basename — matches filepath.Base(Entry.ProjectDir). Unique wins;
//     ambiguous returns AmbiguousError listing candidates with hashes.
//
// Returns os.ErrNotExist (wrapped) when nothing matches. Live and orphan
// entries both participate so callers can act on stale state (purge etc.).
func Resolve(query string) (Entry, error) {
	if query == "" {
		return Entry{}, errors.New("empty query")
	}
	all, err := ListAll()
	if err != nil {
		return Entry{}, err
	}

	// 1. Absolute path / cwd-relative path that exists.
	if abs, err := filepath.Abs(query); err == nil {
		canon := canonical(abs)
		for _, e := range all {
			if canonical(e.ProjectDir) == canon {
				return e, nil
			}
		}
	}

	// 2. Hash prefix.
	if isHashPrefix(query) {
		var matches []Entry
		for _, e := range all {
			if strings.HasPrefix(e.Hash, query) {
				matches = append(matches, e)
			}
		}
		if len(matches) == 1 {
			return matches[0], nil
		}
		if len(matches) > 1 {
			return Entry{}, &AmbiguousError{Query: query, Candidates: matches}
		}
	}

	// 3. Basename match.
	var byBase []Entry
	for _, e := range all {
		if filepath.Base(e.ProjectDir) == query {
			byBase = append(byBase, e)
		}
	}
	switch len(byBase) {
	case 1:
		return byBase[0], nil
	case 0:
		return Entry{}, fmt.Errorf("no project matches %q (run `molt project list`)", query)
	default:
		return Entry{}, &AmbiguousError{Query: query, Candidates: byBase}
	}
}

// isHashPrefix reports whether s looks like a (partial) sha256 hex string:
// at least 4 chars, all lowercase hex. Length 16 is a full hash from Hash().
func isHashPrefix(s string) bool {
	if len(s) < 4 || len(s) > 16 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}
