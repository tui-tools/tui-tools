package main

import (
	"context"

	"github.com/tui-tools/tui-kit/compat"
	"github.com/tui-tools/tui-kit/manifest"
	tuitools "github.com/tui-tools/tui-tools"
)

// probeCompat reads the version of every package manager the launcher can
// drive, and classifies each against what the manifest declares: below the
// minimum, tested, or merely untested.
//
// Three are declared and exactly one of them runs a given machine, so this is
// a probe that is expected to come back mostly empty: apt answers on Debian,
// dnf on Fedora, pacman on Arch. The header shows the one that answered, and
// `installed` below is what drops the other two.
//
// It never fails. A missing binary, a hung process or unparsable output all
// end as a result with no version, because a compatibility probe that can stop
// a tool from starting is worse than no probe.
func probeCompat(ctx context.Context, demo bool) []compat.Result {
	// --demo drives an in-memory machine; probing the real apt or dnf would
	// report a version that has nothing to do with what is on screen.
	if demo {
		return nil
	}
	m, err := manifest.Load(tuitools.ManifestJSON)
	if err != nil {
		return nil
	}
	results := make([]compat.Result, 0, len(m.Backends))
	for _, backend := range m.Backends {
		results = append(results, compat.Probe(ctx, backend))
	}
	return results
}

// installed keeps the backends that answered with a version, which on a normal
// machine is the one package manager it runs. A header row of versions for
// managers that are not installed would be noise.
func installed(results []compat.Result) []compat.Result {
	kept := make([]compat.Result, 0, len(results))
	for _, result := range results {
		if result.Version != "" {
			kept = append(kept, result)
		}
	}
	return kept
}
