package result

import "sort"

// DetermResult is the determinism verdict for one (method, level), derived from
// the compressed-output hashes across runs.
//
// Job concurrency and buffer size do not affect the output bytes (independent
// jobs can't change each other's output; streaming==one-shot), so the meaningful
// axes are:
//
//   - replay: same environment, different runs — does re-running produce the same
//     bytes? Comparable only when >=2 runs of the same env exist.
//   - across arch: same input, different CPU architecture — inherently-parallel
//     codecs may split work by core count and diverge. Comparable only when >=2
//     archs are present.
//
// Determinism across library versions is intentionally not asserted.
type DetermResult struct {
	Method                  string `json:"method"`
	Level                   string `json:"level"`
	ReplayComparable        bool   `json:"replayComparable"`
	DeterministicReplay     bool   `json:"deterministicReplay"`
	ArchComparable          bool   `json:"archComparable"`
	DeterministicAcrossArch bool   `json:"deterministicAcrossArch"`
	Samples                 int    `json:"samples"`
}

// Determinism computes the determinism matrix over a set of runs, looking only at
// compress rows (which carry OutputSHA256).
func Determinism(runs []*Run) []DetermResult {
	type replayAgg struct {
		hashes map[string]struct{}
		runs   map[string]struct{}
	}
	replay := map[string]*replayAgg{}                   // envKey+coord -> hashes/runs
	arch := map[string]map[string]map[string]struct{}{} // coord -> arch -> hashes
	mlSamples := map[mlKey]int{}

	for _, run := range runs {
		envKey := run.Env.Key()
		a := run.Env.Arch
		for i := range run.Rows {
			r := &run.Rows[i]
			if r.Op != "compress" || r.OutputSHA256 == "" {
				continue
			}
			mlSamples[mlKey{r.Method, r.Level}]++
			coord := joinKey(r.Image, r.Method, r.Level)

			rk := joinKey(envKey, coord)
			ra := replay[rk]
			if ra == nil {
				ra = &replayAgg{hashes: map[string]struct{}{}, runs: map[string]struct{}{}}
				replay[rk] = ra
			}
			ra.hashes[r.OutputSHA256] = struct{}{}
			ra.runs[run.RunID] = struct{}{}

			if arch[coord] == nil {
				arch[coord] = map[string]map[string]struct{}{}
			}
			if arch[coord][a] == nil {
				arch[coord][a] = map[string]struct{}{}
			}
			arch[coord][a][r.OutputSHA256] = struct{}{}
		}
	}

	replayOK := map[mlKey]bool{}
	replayCmp := map[mlKey]bool{}
	archOK := map[mlKey]bool{}
	archCmp := map[mlKey]bool{}
	for ml := range mlSamples {
		replayOK[ml] = true
		archOK[ml] = true
	}

	for rk, ra := range replay {
		if len(ra.runs) < 2 {
			continue // can't assert replay from a single run
		}
		ml := mlFromReplayKey(rk)
		replayCmp[ml] = true
		if len(ra.hashes) > 1 {
			replayOK[ml] = false
		}
	}

	for coord, byArch := range arch {
		if len(byArch) < 2 {
			continue
		}
		ml := mlFromCoord(coord)
		archCmp[ml] = true
		all := map[string]struct{}{}
		for _, hs := range byArch {
			for h := range hs {
				all[h] = struct{}{}
			}
		}
		if len(all) > 1 {
			archOK[ml] = false
		}
	}

	out := make([]DetermResult, 0, len(mlSamples))
	for ml, n := range mlSamples {
		out = append(out, DetermResult{
			Method:                  ml.method,
			Level:                   ml.level,
			ReplayComparable:        replayCmp[ml],
			DeterministicReplay:     replayOK[ml] && replayCmp[ml],
			ArchComparable:          archCmp[ml],
			DeterministicAcrossArch: archOK[ml] && archCmp[ml],
			Samples:                 n,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Method != out[j].Method {
			return out[i].Method < out[j].Method
		}
		return out[i].Level < out[j].Level
	})
	return out
}

type mlKey struct{ method, level string }

// coord is image,method,level (sep-joined); replayKey is envKey + coord.

func mlFromCoord(coord string) mlKey {
	f := splitKey(coord) // image,method,level
	return mlKey{f[1], f[2]}
}

func mlFromReplayKey(rk string) mlKey {
	f := splitKey(rk) // envKey,image,method,level
	return mlKey{f[2], f[3]}
}
