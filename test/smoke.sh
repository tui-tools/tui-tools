#!/bin/bash
# Backend smoke test for tui-tools, run inside a lab guest.
#
# The contract (see tui-tools/tui-lab): this script runs on the guest as the
# unprivileged lab user, escalates with `sudo -n` only, prints a short PASS/FAIL
# table and exits non-zero if anything failed. The binary under test is at
# $TUI_LAB_BIN (default: tui-tools on PATH).
#
# What it proves is that the launcher reads the *real* machine — which package
# manager runs here, whether the family repository is configured, which tui-*
# packages are installed — and agrees with the machine's own tooling. It
# installs nothing and removes nothing: the mutations are covered by the unit
# tests against the fake, and a smoke test that installs packages is a smoke
# test nobody can run twice.
#
# Three kinds of machine are asserted, because the family targets all three:
#
#   ubuntu    apt drives it, whatever else the image happens to carry.
#   fedora    dnf drives it.
#   arch      pacman drives it, including on Omarchy Server.
set -uo pipefail

bin="${TUI_LAB_BIN:-tui-tools}"
# TOOL is the manifest name, which is what a compatibility result is keyed on.
TOOL=tui-tools
# The signing key the launcher pins, which --check prints back. It is asserted
# here so a build that quietly carried a different one fails the lab run.
FINGERPRINT=767CFB337B01F32FFC073F3F389120B277E4FB44
pass=0
fail=0

# check runs one assertion. It takes a label, a command and a grep pattern the
# command's output must match. Output is captured so a failure can show it.
check() {
  local label="$1" command="$2" pattern="$3" output status
  output=$(eval "$command" 2>&1)
  status=$?
  if [[ $status -eq 0 ]] && grep -qE "$pattern" <<<"$output"; then
    printf 'PASS  %s\n' "$label"
    pass=$((pass + 1))
  else
    printf 'FAIL  %s (exit %d)\n' "$label" "$status"
    sed 's/^/      | /' <<<"$output" | head -12
    fail=$((fail + 1))
  fi
}

# check_absent is the inverse: the command must succeed and its output must NOT
# contain the pattern. It is what proves something did not happen.
check_absent() {
  local label="$1" command="$2" pattern="$3" output status
  output=$(eval "$command" 2>&1)
  status=$?
  if [[ $status -eq 0 ]] && ! grep -qE "$pattern" <<<"$output"; then
    printf 'PASS  %s\n' "$label"
    pass=$((pass + 1))
  else
    printf 'FAIL  %s (exit %d)\n' "$label" "$status"
    sed 's/^/      | /' <<<"$output" | head -12
    fail=$((fail + 1))
  fi
}

# --- compatibility evidence -------------------------------------------------
#
# The manifest's `tested` lists are generated, not claimed: they are rebuilt
# from compat/results.jsonl by tui-kit/tools/compat-sync.py, and this is where
# the lines of that file come from. The versions recorded are the ones the tool
# itself probed, read back out of --check, so they describe the machine that
# really ran the suite rather than what the tester assumed was installed.
#
# tui-tools declares three backends and exactly one of them runs a given
# machine, so this emits one line: the manager that answered. The other two are
# not installed here and there is nothing to claim about them.
record_compat() {
  local report="$1" outcome="$2" distro today block recorded=0
  block=$(sed -n '/"compat": \[/,/^  \]/p' <<<"$report")
  distro=$(. /etc/os-release && echo "${ID}-${VERSION_ID:-rolling}")
  today=$(date -u +%Y-%m-%d)

  while read -r backend version; do
    [[ -z $backend || -z $version ]] && continue
    local line
    line=$(printf '{"backend":"%s","date":"%s","distro":"%s","result":"%s","suite":"smoke","tool":"%s","version":"%s"}' \
      "$backend" "$today" "$distro" "$outcome" "$TOOL" "$version")
    printf 'compat-result: %s\n' "$line"
    if [[ -n ${TUI_COMPAT_RESULTS:-} ]]; then
      printf '%s\n' "$line" >>"$TUI_COMPAT_RESULTS"
    fi
    recorded=$((recorded + 1))
  done < <(awk '
    /"backend":/ { gsub(/[",]/, ""); b = $2 }
    /"version":/ { gsub(/[",]/, ""); if (b != "") { print b, $2; b = "" } }
  ' <<<"$block")

  if [[ $recorded -eq 0 ]]; then
    echo "      no backend version was probed, so no compatibility result is recorded"
  fi
}

echo "--- tui-tools smoke on $(. /etc/os-release && echo "$PRETTY_NAME")"

family=$(. /etc/os-release && echo "${ID} ${ID_LIKE:-}")
case "$family" in
  *ubuntu* | *debian*) machine=ubuntu ;;
  *fedora* | *rhel*) machine=fedora ;;
  *arch* | *omarchy*) machine=arch ;;
  *) machine=other ;;
esac
echo "      machine=$machine"

report=$("$bin" --check 2>&1)

# 1. The read path works at all, unprivileged. Listing what is installed and
#    what the repositories offer takes no privileges, so this runs as the plain
#    lab user — which is itself the assertion that the launcher does not
#    escalate to look at things it can see without.
check "check reads the machine unprivileged" \
  "$bin --check" \
  '"tool": "tui-tools"'

# 2. The machine is identified, and by the manager that really drives it.
check "the distribution is identified" \
  "$bin --check" \
  '"distro": ".+"'

case "$machine" in
  ubuntu) manager=apt ;;
  fedora) manager=dnf ;;
  arch) manager=pacman ;;
  *) manager='(apt|dnf|pacman)' ;;
esac
check "the package manager is $manager" \
  "$bin --check" \
  "\"manager\": \"$manager\""

# 3. The catalog was read, from somewhere, and it says which.
check "the catalog names its source" \
  "$bin --check" \
  '"source": "(live|snapshot)"'
check "the catalog carries the family" \
  "$bin --check | grep -c '\"package\": \"tui-'" \
  '^([5-9]|[1-9][0-9]+)$'

# 4. Every name that could reach a package manager is one of ours. This is the
#    assertion the whole trust boundary comes down to, checked against the
#    document the machine really fetched rather than against a fixture.
odd=$(grep '"package":' <<<"$report" |
  grep -cvE '"package": "tui-[a-z]+"')
if [[ $odd -eq 0 ]]; then
  printf 'PASS  every package name is tui-<word>\n'
  pass=$((pass + 1))
else
  printf 'FAIL  %d package name(s) are not tui-<word>\n' "$odd"
  fail=$((fail + 1))
fi

# 5. And the launcher is not in its own install list.
check_absent "the launcher does not offer to install itself" \
  "$bin --check" \
  '"package": "tui-tools"'

# 6. Every state is one of the four. The compat block has a "status" of its
#    own, so only the tools array is read.
odd=$(grep '"state":' <<<"$report" |
  grep -cvE '"state": "(not installed|up to date|update available|installed)"')
if [[ $odd -eq 0 ]]; then
  printf 'PASS  every state is one of the four\n'
  pass=$((pass + 1))
else
  printf 'FAIL  %d state(s) are none of the four\n' "$odd"
  fail=$((fail + 1))
fi

# 7. The pinned key is the family's. A build carrying a different fingerprint
#    would set a machine up against the wrong repository.
check "the pinned signing key is the family's" \
  "$bin --check" \
  "\"fingerprint\": \"$FINGERPRINT\""

# 8. What --check says is installed is what the package manager says. The
#    launcher's whole read path is this one claim.
case "$machine" in
  ubuntu) have=$(dpkg-query -W -f '${Package}\n' 'tui-*' 2>/dev/null | sort) ;;
  fedora) have=$(rpm -qa --qf '%{NAME}\n' 'tui-*' 2>/dev/null | sort) ;;
  arch) have=$(pacman -Qq 2>/dev/null | grep '^tui-' | sort) ;;
  *) have="" ;;
esac
# The launcher itself is not in the report, so it is dropped from the machine's
# list before the two are compared.
have=$(grep -v '^tui-tools$' <<<"$have")
said=$(sed -n '/"tools": \[/,$p' <<<"$report" |
  awk '/"package":/ { pkg = $2 } /"installed":/ { print pkg }' |
  tr -d '",' | sort)
if [[ "$(tr -d '[:space:]' <<<"$have")" == "$(tr -d '[:space:]' <<<"$said")" ]]; then
  printf 'PASS  the installed list agrees with the package manager\n'
  pass=$((pass + 1))
else
  printf 'FAIL  the installed list disagrees with the package manager\n'
  printf '      | machine: %s\n' "$(tr '\n' ' ' <<<"$have")"
  printf '      | tool:    %s\n' "$(tr '\n' ' ' <<<"$said")"
  fail=$((fail + 1))
fi

# 9. --demo drives an in-memory machine and says so, so nobody mistakes a demo
#    frame for a report about a real host.
check "demo mode marks itself as one" \
  "$bin --demo --check" \
  '"describe": ".*\(demo\)'

# 10. --offline never reaches the network and says the catalog came from the
#     binary, which is what a machine with no route out falls back to.
check "offline reads the embedded snapshot" \
  "$bin --offline --check" \
  '"source": "snapshot"'

# 11. The exit code is not the verdict: a machine with nothing installed is
#     still a successful run of the launcher.
"$bin" --check >/dev/null 2>&1
status=$?
if [[ $status -eq 0 ]]; then
  printf 'PASS  --check exits 0 whatever the machine has\n'
  pass=$((pass + 1))
else
  printf 'FAIL  --check exited %d; what the machine has belongs in the report\n' "$status"
  fail=$((fail + 1))
fi

# 12. And it must have changed nothing: not the installed set, not the
#     repository configuration. This is the assertion that makes --check safe
#     to run against a machine somebody depends on.
before_pkgs=$(case "$machine" in
  ubuntu) dpkg-query -W -f '${Package} ${Version}\n' 'tui-*' 2>/dev/null | sort ;;
  fedora) rpm -qa --qf '%{NAME} %{VERSION}\n' 'tui-*' 2>/dev/null | sort ;;
  arch) pacman -Q 2>/dev/null | grep '^tui-' | sort ;;
esac)
before_repo=$(ls /etc/apt/sources.list.d/tui-tools.list \
  /etc/yum.repos.d/tui-tools.repo /etc/pacman.d/tui-tools.conf 2>/dev/null | sort)
"$bin" --check >/dev/null 2>&1
after_pkgs=$(case "$machine" in
  ubuntu) dpkg-query -W -f '${Package} ${Version}\n' 'tui-*' 2>/dev/null | sort ;;
  fedora) rpm -qa --qf '%{NAME} %{VERSION}\n' 'tui-*' 2>/dev/null | sort ;;
  arch) pacman -Q 2>/dev/null | grep '^tui-' | sort ;;
esac)
after_repo=$(ls /etc/apt/sources.list.d/tui-tools.list \
  /etc/yum.repos.d/tui-tools.repo /etc/pacman.d/tui-tools.conf 2>/dev/null | sort)
if [[ "$before_pkgs" == "$after_pkgs" && "$before_repo" == "$after_repo" ]]; then
  printf 'PASS  --check left the machine untouched\n'
  pass=$((pass + 1))
else
  printf 'FAIL  --check changed the machine\n'
  printf '      | packages: %s -> %s\n' "$before_pkgs" "$after_pkgs"
  printf '      | repo:     %s -> %s\n' "$before_repo" "$after_repo"
  fail=$((fail + 1))
fi

if [[ $fail -eq 0 ]]; then
  record_compat "$report" pass
else
  record_compat "$report" fail
fi

echo "--- tui-tools: $pass passed, $fail failed"
[[ $fail -eq 0 ]]
