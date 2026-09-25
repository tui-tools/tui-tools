package packages

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/tui-tools/tui-kit/pkgmgr"
	"github.com/tui-tools/tui-tools/internal/catalog"
)

// This file is the companion half of the backend: the family's packages that
// are not terminal UIs.
//
// A companion's name is the upstream project's own ("headscale") or a
// tui-tools-<something> component, so the ^tui-[a-z]+$ rule the kit holds a
// tool to does not apply. The kit's companion builders cover them
// (pkgmgr.CheckCompanionName, pkgmgr.BuildCompanionInstallOn and the rest):
// the same argv shapes the tools get, behind a companion name check. What is
// left here is what only this launcher does: switching a machine to the
// family's build of an upstream project, and reading where an installed copy
// came from. The commands are still values, still previewed by the caller and
// still executed by the kit runner, which remains the single place a process
// is started.

// RepoName is the family repository's name in every manager's configuration.
// It is what a mirror's package has to come from for the family's signing and
// provenance gate to mean anything on this machine.
const RepoName = pkgmgr.DefaultRepoName

// RepoHost is the host apt reports as the origin of a package that came from
// the family repository. apt names an origin by its URL rather than by a
// repository name, so this is what the origin lines are matched against.
const RepoHost = "pkgs.tui.tools"

// ProvenanceLine is the one sentence a preview has to carry before a machine is
// switched to the family's build of an upstream project.
const ProvenanceLine = "rebuilt from source under the family signing and " +
	"provenance gate"

// CompanionTargets names each companion in the family repository,
// `tui-tools/<name>`, which is the target form the kit's install and upgrade
// builders take. pacman gets it as written: a mirror such as headscale is also
// in Arch's own repositories, which pacman.conf lists before the family's, so
// a bare name would install the distribution's build while the dialog
// promised the family's. apt and dnf get the bare name from the kit (apt would
// read the qualifier as a release, and a dnf --repo would hide the
// distribution's repositories from dependency resolution).
func CompanionTargets(names []string) []string {
	out := make([]string, len(names))
	for i, name := range names {
		out[i] = RepoName + "/" + name
	}
	return out
}

// BuildCompanionSwitch builds the steps that replace an installed package with
// the family's own build of it.
//
// The origin is the one the probe reported, and it carries the version the
// family repository offers, which apt needs on the command line: apt has no
// "install this from that repository" flag, so the family's build is named by
// its version and reached through the repository priority the setup already
// wrote. pacman and dnf can name the repository directly.
func BuildCompanionSwitch(manager pkgmgr.Manager, name string,
	origin catalog.Origin) ([]pkgmgr.Command, error) {
	if err := pkgmgr.CheckCompanionName(name); err != nil {
		return nil, err
	}
	if !origin.Offered {
		return nil, fmt.Errorf(
			"packages: the %s repository does not carry %s", RepoName, name)
	}
	switch manager {
	case pkgmgr.ManagerAPT:
		if !ValidVersion(origin.Version) {
			return nil, fmt.Errorf(
				"packages: the %s repository reported no usable version for %s, "+
					"so apt cannot be told which build to install", RepoName, name)
		}
		refresh, err := pkgmgr.BuildRefresh(manager)
		if err != nil {
			return nil, err
		}
		return []pkgmgr.Command{refresh, {
			Argv: []string{
				"apt-get", "install", "-y", "--allow-downgrades",
				name + "=" + origin.Version,
			},
			// The same noninteractive environment as every other apt
			// mutation (pkgmgr.APTEnv): no debconf or needrestart prompt can
			// wait behind the TUI.
			Env:        pkgmgr.APTEnv(),
			Privileged: true,
			Explain: "Install the " + RepoName + " build of " + name +
				", version " + origin.Version,
		}}, nil
	case pkgmgr.ManagerDNF:
		return []pkgmgr.Command{{
			Argv:       []string{"dnf", "install", "--repo", RepoName, "-y", name},
			Privileged: true,
			Explain:    "Install " + name + " from the " + RepoName + " repository",
		}}, nil
	case pkgmgr.ManagerPacman:
		return []pkgmgr.Command{{
			Argv:       []string{"pacman", "-S", "--noconfirm", RepoName + "/" + name},
			Privileged: true,
			Explain:    "Install " + name + " from the " + RepoName + " repository",
		}}, nil
	default:
		return nil, unknownManager(manager)
	}
}

// versionRe is the shape a version may have before it is written into an argv.
// It is checked because the version comes from a package manager's output,
// which is one more thing this tool did not write.
var versionRe = regexp.MustCompile(`^[0-9][A-Za-z0-9.+~:_-]{0,63}$`)

// ValidVersion reports whether a version read off a manager's output is one a
// command line may carry.
func ValidVersion(version string) bool { return versionRe.MatchString(version) }

// unknownManager is a manager this build has no commands for.
func unknownManager(manager pkgmgr.Manager) error {
	return fmt.Errorf("packages: unknown package manager %q", manager)
}

// ------------------------------------------------------------- provenance ---

// BuildOriginProbes builds the read-only commands that answer where the
// installed copy of each companion package came from, and whether the family
// repository offers it.
//
// Every one of them reads metadata already on disk and needs no privilege.
// Nothing here refreshes anything: the answer is about the machine as it
// stands, and a refresh is a previewed step of its own.
//
// The commands differ per manager because the managers differ in what they
// record. dnf keeps the repository a package was installed from and simply
// answers the question. apt keeps an origin table per version, so the answer is
// read off the entry the installed version is marked in. pacman keeps no
// from-repo field at all, which is why its probe takes three commands and ends
// in an inference rather than a fact; parsePacmanOrigins says how.
func BuildOriginProbes(manager pkgmgr.Manager,
	names []string) ([]pkgmgr.Command, error) {
	if err := pkgmgr.CheckCompanionNames(names); err != nil {
		return nil, err
	}
	switch manager {
	case pkgmgr.ManagerAPT:
		return []pkgmgr.Command{{
			Argv:    append([]string{"apt-cache", "policy"}, names...),
			Explain: "Read which repository each installed version came from",
		}}, nil
	case pkgmgr.ManagerDNF:
		// dnf4 and dnf5 both accept these flags and both expand %{from_repo},
		// so one command serves the two. dnf5 renamed --queryformat to --qf and
		// kept --qf working on dnf4, which is why the short form is the one
		// written here.
		return []pkgmgr.Command{{
			Argv: append([]string{
				"dnf", "--quiet", "repoquery", "--installed",
				"--qf", `%{name}|%{from_repo}` + "\n",
			}, names...),
			Explain: "Read which repository each installed package came from",
		}, {
			Argv: append([]string{
				"dnf", "--quiet", "repoquery", "--repo", RepoName,
				"--qf", `%{name}|%{evr}` + "\n",
			}, names...),
			Explain: "Read what the " + RepoName + " repository offers",
		}}, nil
	case pkgmgr.ManagerPacman:
		return []pkgmgr.Command{{
			Argv:    []string{"pacman", "-Sl", RepoName},
			Explain: "List what the " + RepoName + " repository carries",
		}, {
			Argv:    append([]string{"pacman", "-Qi"}, names...),
			Explain: "Read the installed versions from the pacman database",
		}, {
			Argv:    append([]string{"pacman", "-Si"}, names...),
			Explain: "Read which configured repository offers which version",
		}}, nil
	default:
		return nil, unknownManager(manager)
	}
}

// ParseOrigins reads what BuildOriginProbes printed, in the same order the
// probes were built, and returns one Origin per name. A name nothing answered
// for is absent from the map, which the row reads as "cannot say".
func ParseOrigins(manager pkgmgr.Manager, names []string,
	outputs []string) map[string]catalog.Origin {
	switch manager {
	case pkgmgr.ManagerAPT:
		return parseAPTOrigins(names, at(outputs, 0))
	case pkgmgr.ManagerDNF:
		return parseDNFOrigins(names, at(outputs, 0), at(outputs, 1))
	case pkgmgr.ManagerPacman:
		return parsePacmanOrigins(names, at(outputs, 0), at(outputs, 1),
			at(outputs, 2))
	default:
		return map[string]catalog.Origin{}
	}
}

// at returns one probe's output, or nothing when the probe did not run.
func at(outputs []string, i int) string {
	if i < 0 || i >= len(outputs) {
		return ""
	}
	return outputs[i]
}

// parseDNFOrigins reads `dnf repoquery --installed --qf '%{name}|%{from_repo}'`
// and the same query against the family repository.
//
// dnf is the one manager that records where an installed package came from, so
// there is no inference here: from_repo is the answer. It is empty, or
// "@System", for a package installed from a file rather than a repository.
func parseDNFOrigins(names []string, installedOut, offeredOut string) map[string]catalog.Origin {
	from := pkgmgr.ParsePipedVersions(installedOut)
	offered := pkgmgr.ParsePipedVersions(offeredOut)

	origins := map[string]catalog.Origin{}
	for _, name := range names {
		origin := catalog.Origin{}
		if version, ok := offered[name]; ok {
			origin.Offered = true
			origin.Version = version
		}
		repo := from[name]
		if repo == "@System" || repo == "@commandline" {
			repo = ""
		}
		origin.Repo = repo
		origin.Family = repo == RepoName
		origin.Detail = dnfDetail(name, repo)
		origins[name] = origin
	}
	return origins
}

// dnfDetail says how the dnf answer was reached.
func dnfDetail(name, repo string) string {
	if repo == "" {
		return "dnf records no repository for the installed " + name +
			", so it was installed from a file rather than from a repository"
	}
	return "dnf records " + repo + " as the repository the installed " + name +
		" came from"
}

// parseAPTOrigins reads `apt-cache policy`, whose block per package carries a
// version table: one entry per version, the installed one marked `***`, and
// under each entry the origins that offer it.
//
// The origin of the installed version is the first source under the marked
// entry that is not the dpkg status file, because /var/lib/dpkg/status is the
// machine's own record rather than a repository.
func parseAPTOrigins(names []string, out string) map[string]catalog.Origin {
	blocks := aptPolicyBlocks(out)
	origins := map[string]catalog.Origin{}
	for _, name := range names {
		block, ok := blocks[name]
		if !ok {
			continue
		}
		origin := catalog.Origin{}
		for _, entry := range block {
			if entry.family() && !origin.Offered {
				origin.Offered = true
				origin.Version = entry.version
			}
			if !entry.installed {
				continue
			}
			source := entry.source()
			switch {
			case source == "":
				origin.Detail = "no configured repository offers the installed " +
					name + ", so where it came from cannot be said"
			case strings.Contains(source, RepoHost):
				origin.Repo = RepoName
				origin.Family = true
				origin.Detail = "apt-cache policy marks " + entry.version +
					" as installed from " + source
			default:
				origin.Repo = sourceHost(source)
				origin.Detail = "apt-cache policy marks " + entry.version +
					" as installed from " + source
			}
		}
		origins[name] = origin
	}
	return origins
}

// aptEntry is one version of one package in an apt-cache policy version table:
// the version, whether it is the installed one, and the sources that offer it.
type aptEntry struct {
	version   string
	installed bool
	sources   []string
}

// family reports whether the family repository offers this version.
func (e aptEntry) family() bool {
	for _, source := range e.sources {
		if strings.Contains(source, RepoHost) {
			return true
		}
	}
	return false
}

// source is the repository this version came from: the first source that is not
// the machine's own dpkg record.
func (e aptEntry) source() string {
	for _, source := range e.sources {
		if strings.HasPrefix(source, "/") {
			continue
		}
		return source
	}
	return ""
}

// aptPolicyBlocks splits apt-cache policy output into one version table per
// package name.
func aptPolicyBlocks(out string) map[string][]aptEntry {
	blocks := map[string][]aptEntry{}
	name := ""
	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		// A package header is flush left and ends in a colon.
		if !strings.HasPrefix(line, " ") && strings.HasSuffix(trimmed, ":") {
			name = strings.TrimSuffix(trimmed, ":")
			continue
		}
		if name == "" {
			continue
		}
		fields := strings.Fields(trimmed)
		switch {
		case len(fields) >= 2 && isDigits(fields[0]):
			// An origin line: a priority, then where the version comes from.
			entries := blocks[name]
			if len(entries) == 0 {
				continue
			}
			entries[len(entries)-1].sources = append(
				entries[len(entries)-1].sources, strings.Join(fields[1:], " "))
			blocks[name] = entries
		case len(fields) >= 2 && fields[0] == "***":
			blocks[name] = append(blocks[name],
				aptEntry{version: fields[1], installed: true})
		case len(fields) == 2 && isDigits(fields[1]):
			blocks[name] = append(blocks[name], aptEntry{version: fields[0]})
		}
	}
	return blocks
}

// isDigits reports whether a field is a plain number, which is how an origin
// line is told apart from a version line: apt prints a priority first on one
// and a version first on the other.
func isDigits(field string) bool {
	_, err := strconv.Atoi(field)
	return err == nil
}

// sourceHost names an apt origin the short way, by the host it is served from,
// so a row can show it without a line of URL.
func sourceHost(source string) string {
	fields := strings.Fields(source)
	if len(fields) == 0 {
		return source
	}
	url := fields[0]
	for _, scheme := range []string{"https://", "http://", "ftp://"} {
		if rest, ok := strings.CutPrefix(url, scheme); ok {
			host, _, _ := strings.Cut(rest, "/")
			return host
		}
	}
	return url
}

// parsePacmanOrigins reads the three pacman probes and infers where the
// installed package came from.
//
// pacman records no from-repo field: a package on the machine carries its name
// and its version, and nothing that says which repository handed it over. So
// the answer is an inference from version equality, and it is drawn twice over:
//
//   - `pacman -Sl tui-tools` prints a bare `[installed]` beside a package whose
//     installed version is the one this repository offers, and
//     `[installed: <other>]` when the machine has a different one. A bare
//     marker is therefore the family repository claiming the copy on the disk.
//   - `pacman -Si <name>` prints one block per configured repository that
//     offers the package. The repository whose version equals the installed one
//     is the one it plausibly came from, and it is what the row names.
//
// Two repositories offering the identical version cannot be told apart, which
// is why the detail line says the answer was inferred rather than read.
func parsePacmanOrigins(names []string, listOut, infoOut,
	syncOut string) map[string]catalog.Origin {
	offered, sameVersion := parsePacmanList(listOut)
	installed := pacmanVersions(infoOut)
	repos := pacmanRepoVersions(syncOut)

	origins := map[string]catalog.Origin{}
	for _, name := range names {
		origin := catalog.Origin{}
		if version, ok := offered[name]; ok {
			origin.Offered = true
			origin.Version = version
		}
		here, isInstalled := installed[name]
		switch {
		case !isInstalled:
			origin.Detail = name + " is not installed, so nothing on this " +
				"machine has an origin"
		case origin.Offered && sameVersion[name]:
			origin.Repo = RepoName
			origin.Family = true
			origin.Detail = "pacman -Sl " + RepoName + " reports " + name +
				" " + origin.Version + " as the installed one, so the copy " +
				"here is the family's"
		default:
			if repo := pacmanRepoOf(repos[name], here); repo != "" {
				origin.Repo = repo
				origin.Detail = repo + " offers " + name + " " + here +
					", which is the installed version, so it is where the " +
					"copy here came from"
			} else {
				origin.Detail = "no configured repository offers " + name +
					" " + here + ", so where the copy here came from cannot " +
					"be said"
			}
		}
		origins[name] = origin
	}
	return origins
}

// parsePacmanList reads `pacman -Sl <repo>`, whose every line is
// `<repo> <name> <version>` plus, for a package the machine has, `[installed]`
// when the versions match and `[installed: <version>]` when they do not.
func parsePacmanList(out string) (map[string]string, map[string]bool) {
	offered := map[string]string{}
	sameVersion := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) < 3 {
			continue
		}
		name, version := fields[1], fields[2]
		offered[name] = version
		if len(fields) >= 4 && fields[3] == "[installed]" {
			sameVersion[name] = true
		}
	}
	return offered, sameVersion
}

// pacmanVersions reads the Name and Version fields of `pacman -Qi` blocks.
func pacmanVersions(out string) map[string]string {
	versions := map[string]string{}
	for _, block := range pacmanBlocks(out) {
		if block["Name"] != "" && block["Version"] != "" {
			versions[block["Name"]] = block["Version"]
		}
	}
	return versions
}

// pacmanFamilyVersions reads `pacman -Si` and keeps the version the family
// repository offers for each package, which is the version an install or an
// upgrade from this launcher fetches: on pacman those name every companion as
// tui-tools/<name> (CompanionTargets).
//
// The kit's own reader keeps the last block, and for a tool that is the same
// answer: a tui-<word> package exists in one repository. A companion carries
// the upstream project's name, so several repositories can offer it, and the
// distribution's come first in pacman.conf. Neither first nor last is the
// answer then; the block whose Repository is the family's is. A companion the
// family repository does not carry has no available version, which is true:
// nothing this launcher builds could install it.
func pacmanFamilyVersions(out string) map[string]string {
	versions := map[string]string{}
	for name, repos := range pacmanRepoVersions(out) {
		if version, ok := repos[RepoName]; ok {
			versions[name] = version
		}
	}
	return versions
}

// pacmanRepoVersions reads `pacman -Si`, which prints one block per repository
// that offers a package, and keeps the version each repository offers.
func pacmanRepoVersions(out string) map[string]map[string]string {
	repos := map[string]map[string]string{}
	for _, block := range pacmanBlocks(out) {
		name, repo, version := block["Name"], block["Repository"], block["Version"]
		if name == "" || repo == "" || version == "" {
			continue
		}
		if repos[name] == nil {
			repos[name] = map[string]string{}
		}
		repos[name][repo] = version
	}
	return repos
}

// pacmanRepoOf names the repository that offers a version, or nothing when
// none does.
func pacmanRepoOf(repos map[string]string, version string) string {
	for repo, offered := range repos {
		if offered == version {
			return repo
		}
	}
	return ""
}

// pacmanBlocks splits `Field : value` output into one map per blank-line
// separated block. Only the first occurrence of a field is kept, so a
// continuation line cannot overwrite the value it continues.
func pacmanBlocks(out string) []map[string]string {
	var blocks []map[string]string
	current := map[string]string{}
	flush := func() {
		if len(current) > 0 {
			blocks = append(blocks, current)
			current = map[string]string{}
		}
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		field, value, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		field, value = strings.TrimSpace(field), strings.TrimSpace(value)
		// Every field this parser reads is one word. A continuation line of a
		// multi-line field can carry a colon of its own, and dropping anything
		// that is not one word is what stops it from being read as a field.
		if field == "" || strings.ContainsAny(field, " \t") {
			continue
		}
		if _, seen := current[field]; !seen {
			current[field] = value
		}
	}
	flush()
	return blocks
}

// ------------------------------------------------------------- read paths ---

// CompanionVersions reads what the machine says about the companion packages:
// which are installed, and which version the repositories would install.
//
// A repository that carries nothing, or none at all, is an answer rather than a
// failure, so the available map comes back empty and the rows say "installed,
// cannot say" instead of the screen going blank.
func CompanionVersions(ctx context.Context, backend Backend,
	names []string) (installed, available map[string]string) {
	manager := backend.Manager()
	installed = map[string]string{}
	available = map[string]string{}
	if len(names) == 0 {
		return installed, available
	}
	if cmd, err := pkgmgr.BuildCompanionInstalled(manager, names); err == nil {
		out, _ := backend.Run(ctx, cmd)
		installed = parseVersions(manager, out, false)
	}
	if cmd, err := pkgmgr.BuildCompanionAvailable(manager, names); err == nil {
		out, _ := backend.Run(ctx, cmd)
		available = parseVersions(manager, out, true)
	}
	return installed, available
}

// parseVersions reads a versions query, in the shape the manager answers it.
func parseVersions(manager pkgmgr.Manager, out string, sync bool) map[string]string {
	switch manager {
	case pkgmgr.ManagerPacman:
		if sync {
			return pacmanFamilyVersions(out)
		}
		return pkgmgr.ParsePacmanQuery(out)
	case pkgmgr.ManagerAPT:
		if sync {
			return pkgmgr.ParseAPTPolicy(out)
		}
		return pkgmgr.ParseDpkgStatus(out)
	default:
		return pkgmgr.ParsePipedVersions(out)
	}
}

// CompanionOrigins reads where each installed companion package came from.
//
// It is only worth asking about packages that are here: an origin is a fact
// about a copy on the disk, and there is no copy of a package that is not
// installed. The probes are read-only, so a failure is not fatal — the map
// simply has no entry, and the row says nothing rather than guessing.
func CompanionOrigins(ctx context.Context, backend Backend,
	names []string) map[string]catalog.Origin {
	if len(names) == 0 {
		return map[string]catalog.Origin{}
	}
	manager := backend.Manager()
	probes, err := BuildOriginProbes(manager, names)
	if err != nil {
		return map[string]catalog.Origin{}
	}
	outputs := make([]string, 0, len(probes))
	for _, probe := range probes {
		out, _ := backend.Run(ctx, probe)
		outputs = append(outputs, out)
	}
	return ParseOrigins(manager, names, outputs)
}
