package syncplan

import (
	"fmt"
	"os"
	"path/filepath"

	"molt/internal/lockparse"
	"molt/internal/projstate"
	"molt/internal/store"
)

// GC scans every project in the registry, collects the wheel-key set
// referenced by each project's uv.lock, and removes any entries from the
// global store that nobody references. Projects whose directory has
// disappeared are dropped from the registry.
func GC(dryRun bool) error {
	st, err := store.Default()
	if err != nil {
		return err
	}
	r, err := loadRegistry()
	if err != nil {
		return err
	}

	live := map[string]struct{}{} // store-relative dir, e.g. "requests/2.31.0/py3-none-any"
	staleProjects := []string{}

	for proj := range r.Projects {
		if _, err := os.Stat(proj); os.IsNotExist(err) {
			staleProjects = append(staleProjects, proj)
			continue
		}
		lockPath := filepath.Join(proj, "uv.lock")
		pkgs, err := lockparse.Parse(lockPath)
		if err != nil {
			// Can't parse this project's lock — be conservative, treat all current entries as live.
			fmt.Fprintf(os.Stderr, "warn: skipping %s: %v\n", proj, err)
			continue
		}
		for _, p := range pkgs {
			for _, w := range p.Wheels {
				key, err := store.ParseWheelFilename(w.Filename)
				if err != nil {
					continue
				}
				rel := filepath.Join(store.NormalizeName(p.Name), p.Version,
					key.PyTag+"-"+key.AbiTag+"-"+key.PlatformTag)
				live[rel] = struct{}{}
			}
		}
	}

	// Walk the store, top three levels: name/version/tag.
	candidates := []string{}
	nameDirs, _ := os.ReadDir(st.Root)
	for _, n := range nameDirs {
		if !n.IsDir() || n.Name() == ".tmp" || n.Name() == ".dl" {
			continue
		}
		verDirs, _ := os.ReadDir(filepath.Join(st.Root, n.Name()))
		for _, v := range verDirs {
			if !v.IsDir() {
				continue
			}
			tagDirs, _ := os.ReadDir(filepath.Join(st.Root, n.Name(), v.Name()))
			for _, t := range tagDirs {
				if !t.IsDir() {
					continue
				}
				rel := filepath.Join(n.Name(), v.Name(), t.Name())
				if _, ok := live[rel]; !ok {
					candidates = append(candidates, rel)
				}
			}
		}
	}

	// Find orphaned per-project state dirs under ~/.molt/projects/. An
	// entry is orphaned when its meta.json points at a project path that
	// no longer has a pyproject.toml.
	orphanStates := []projstate.Entry{}
	if entries, err := projstate.ListAll(); err == nil {
		for _, e := range entries {
			if e.ProjectAlive {
				continue
			}
			orphanStates = append(orphanStates, e)
		}
	}

	if dryRun {
		fmt.Printf("would remove %d store entr%s; would drop %d stale project(s); would prune %d orphan state dir(s):\n",
			len(candidates), pluralS(len(candidates)), len(staleProjects), len(orphanStates))
		for _, c := range candidates {
			fmt.Printf("  - %s\n", filepath.Join(st.Root, c))
		}
		for _, p := range staleProjects {
			fmt.Printf("  - registry: %s\n", p)
		}
		for _, e := range orphanStates {
			origin := e.ProjectDir
			if origin == "" {
				origin = "(no meta.json)"
			}
			fmt.Printf("  - state: %s  (was %s)\n", e.Dir, origin)
		}
		return nil
	}

	release, err := st.Lock()
	if err != nil {
		return err
	}
	defer release()

	for _, c := range candidates {
		full := filepath.Join(st.Root, c)
		if err := os.RemoveAll(full); err != nil {
			fmt.Fprintf(os.Stderr, "warn: remove %s: %v\n", full, err)
		}
		// Try to clean up empty parent dirs.
		_ = os.Remove(filepath.Dir(full))
		_ = os.Remove(filepath.Dir(filepath.Dir(full)))
	}
	for _, p := range staleProjects {
		delete(r.Projects, p)
	}
	for _, e := range orphanStates {
		if err := os.RemoveAll(e.Dir); err != nil {
			fmt.Fprintf(os.Stderr, "warn: remove state %s: %v\n", e.Dir, err)
		}
	}
	if err := saveRegistry(r); err != nil {
		return err
	}
	fmt.Printf("✓ removed %d store entr%s, dropped %d stale project(s), pruned %d orphan state dir(s)\n",
		len(candidates), pluralS(len(candidates)), len(staleProjects), len(orphanStates))
	return nil
}

func pluralS(n int) string {
	if n == 1 {
		return "y"
	}
	return "ies"
}
