#!/usr/bin/env bash
# Auto-merge open pull requests whose CI has passed.
#
# Runs from .github/workflows/automerge.yml on the DEFAULT BRANCH (via
# workflow_run and schedule), so a pull request cannot change this script or
# the rules it applies. A PR is squash-merged only when ALL of these hold:
#
#   - it targets main, is open, is not a draft, and has no hold label;
#   - it is not from a bot account (unless ALLOW_BOTS=true);
#   - it does not touch PROTECTED_PATHS (CI, release and build definitions:
#     a PR runs its own copy of the workflows, so one that edits them could
#     make its own checks pass) and does not delete tests;
#   - every check in REQUIRED_CHECKS ran on its head commit and succeeded,
#     and every other check on that commit finished without failing;
#   - those checks ran after main's current head commit, so they tested the
#     PR against the code it would actually merge into;
#   - GitHub reports it mergeable with no conflicts.
#   - main's .github/scripts/malware-scan.sh finds nothing in what the PR
#     adds (blocking patterns and anything needing human review both hold).
#
# Anything else is left for a maintainer, with one explanatory comment and a
# label, so the author knows why. At most one PR is merged per run, because a
# merge moves main and makes every other PR's checks stale.
#
# Environment:
#   GH_TOKEN          token with contents:write and pull-requests:write
#   GITHUB_REPOSITORY owner/name (set by Actions)
#   REQUIRED_CHECKS   space-separated check names that must pass; a name also
#                     covers its matrix runs, so "test" requires every
#                     "test (...)" run to pass
#   PROTECTED_PATHS   space-separated path prefixes a PR must not touch
#   CI_WORKFLOW       workflow file to run on main after a merge (optional)
#   ALLOW_BOTS        "true" to also merge bot-authored PRs (default false)
#   DRY_RUN           "true" to report decisions without merging
set -euo pipefail

for tool in gh jq awk; do
  command -v "$tool" >/dev/null || { echo "automerge: $tool is required" >&2; exit 1; }
done

repo="${GITHUB_REPOSITORY:?}"
required="${REQUIRED_CHECKS:-}"
protected="${PROTECTED_PATHS:-.github/}"
allow_bots="${ALLOW_BOTS:-false}"
dry_run="${DRY_RUN:-false}"

hold_label="needs-maintainer-review"
stale_label="needs-update"
marker="<!-- automerge-bot -->"

log() { printf '%s\n' "$*"; }

ensure_label() {
  [ "$dry_run" = true ] && return
  gh label create "$1" -R "$repo" -c "$2" -d "$3" >/dev/null 2>&1 || true
}

# comment_once posts a comment unless the bot already left the same reason.
comment_once() {
  local pr="$1" reason="$2" body="$3"
  local tag="${marker}<!-- reason:${reason} -->"
  if gh api "repos/$repo/issues/$pr/comments" --paginate --jq '.[].body' | grep -qF "$tag"; then
    return
  fi
  if [ "$dry_run" = true ]; then
    log "  would comment ($reason)"
    return
  fi
  gh pr comment "$pr" -R "$repo" --body "$tag
$body" >/dev/null
}

# matching_checks prints the distinct check names on the current PR that a
# required name covers: the name itself and its matrix runs "<name> (...)".
matching_checks() {
  awk -F'\t' -v n="$1" '$1 == n || index($1, n " (") == 1 { print $1 }' <<<"$checks" | sort -u
}

hold() {
  local pr="$1" label="$2" reason="$3" body="$4"
  log "  HOLD #$pr: $reason"
  [ "$dry_run" = true ] || gh pr edit "$pr" -R "$repo" --add-label "$label" >/dev/null 2>&1 || true
  comment_once "$pr" "$reason" "$body"
}

if [ -z "$required" ]; then
  log "REQUIRED_CHECKS is empty: this repository has no CI to gate on, so nothing is merged automatically."
  exit 0
fi

ensure_label "$hold_label" "d93f0b" "Auto-merge skipped this PR; a maintainer must review it"
ensure_label "$stale_label" "fbca04" "CI ran against an older main; update the branch to re-run it"

main_sha=$(gh api "repos/$repo/commits/main" --jq '.sha')
main_time=$(gh api "repos/$repo/commits/main" --jq '.commit.committer.date')
log "main is $main_sha ($main_time)"

# Merging one PR moves main, which used to make every other PR stale until
# its CI re-ran. With dozens of PRs queued that serialises everything behind
# CI. Instead, a PR whose checks predate main may still merge when nothing
# main gained since the PR's merge base touches the same directories (Go
# packages) as the PR, and main itself is green. Anything that could change a
# PR's build — go.mod/go.sum or CI definitions on main — still forces a
# re-run. Up to MAX_MERGES non-overlapping PRs merge per run; CI on main runs
# once afterwards, and a red main stops relaxed merging until it is fixed.
max_merges="${MAX_MERGES:-8}"
merges=0
main_ci_ok=false
if [ -n "${CI_WORKFLOW:-}" ]; then
  ci_state=$(gh run list -R "$repo" --workflow "$CI_WORKFLOW" --branch main --limit 20 \
    --json headSha,status,conclusion \
    --jq "[.[] | select(.headSha == \"$main_sha\")][0] | \"\(.status) \(.conclusion)\"" 2>/dev/null || true)
  [ "$ci_state" = "completed success" ] && main_ci_ok=true
  log "CI on main: ${ci_state:-not run}; relaxed freshness $([ "$main_ci_ok" = true ] && echo on || echo off)"
fi
git fetch -q --no-tags origin main || true

# dirs_of prints the distinct directories of the given paths, ignoring
# documentation, which cannot change what a PR builds or tests.
dirs_of() { grep -vE '\.(md|txt)$' | sed -E 's#/[^/]*$##; t; s#.*#.#' | sort -u; }

# unaffected_by_main reports whether main has changed nothing since the PR's
# merge base that could alter the PR's build: no shared directory, and no
# module or workflow definition change. Main is read from origin/main, which is
# refreshed after every merge in this run.
unaffected_by_main() {
  local head="$1" pr_files="$2" base changed
  git fetch -q --no-tags origin "+refs/pull/$pr/head:refs/automerge/pr-$pr" 2>/dev/null || return 1
  base=$(git merge-base origin/main "$head" 2>/dev/null) || return 1
  changed=$(git diff --name-only "$base" origin/main)
  [ -z "$changed" ] && return 0
  # Workflow files, not the bot's own scripts, decide how a PR is built.
  grep -qE '(^|/)go\.(mod|sum)$|^\.github/workflows/|(^|/)package(-lock)?\.json$' <<<"$changed" && return 1
  [ -z "$(comm -12 <(dirs_of <<<"$changed") <(dirs_of <<<"$pr_files"))" ]
}

prs=$(gh pr list -R "$repo" --state open --base main --limit 100 \
  --json number,isDraft,headRefOid,author,labels,title)

for pr in $(jq -r '.[].number' <<<"$prs"); do
  row=$(jq -c --argjson n "$pr" '.[] | select(.number == $n)' <<<"$prs")
  sha=$(jq -r '.headRefOid' <<<"$row")
  author=$(jq -r '.author.login' <<<"$row")
  is_bot=$(jq -r '.author.is_bot // false' <<<"$row")
  log "PR #$pr by $author at $sha"

  if [ "$(jq -r '.isDraft' <<<"$row")" = true ]; then
    log "  skip: draft"; continue
  fi
  if jq -e --arg l "$hold_label" '[.labels[].name] | index($l)' <<<"$row" >/dev/null || \
     jq -e '[.labels[].name] | index("do-not-merge")' <<<"$row" >/dev/null; then
    log "  skip: on hold"; continue
  fi
  if [ "$allow_bots" != true ] && { [ "$is_bot" = true ] || [[ "$author" == *"[bot]" ]] || [[ "$author" == app/* ]]; }; then
    log "  skip: bot-authored"; continue
  fi

  # Files: protected paths and deleted tests both need a human.
  #
  # The list comes from comparing main's current head with the PR head, not
  # from the PR's files endpoint: that endpoint diffs against the base commit
  # GitHub recorded for the PR, which can lag behind main, so a PR that merged
  # main would be charged with main's own changes (e.g. to .github/).
  files=$(gh api "repos/$repo/compare/$main_sha...$sha" \
    --jq '.files[] | "\(.status) \(.filename)"')
  touched=""
  for prefix in $protected; do
    hit=$(awk -v p="$prefix" 'index($2, p) == 1 { print $2 }' <<<"$files" | head -5)
    [ -n "$hit" ] && touched="$touched $hit"
  done
  if [ -n "$touched" ]; then
    hold "$pr" "$hold_label" "protected-paths" "Auto-merge skipped this PR because it changes files that define how CI, builds or releases run:
$(for f in $touched; do printf -- '- `%s`\n' "$f"; done)
A pull request runs its own copy of these files, so a change to them could alter the checks that would approve it. A maintainer will review it."
    continue
  fi
  deleted_tests=$(awk '$1 == "removed" && ($2 ~ /_test\.go$/ || $2 ~ /(^|\/)testdata\//) { print $2 }' <<<"$files" | head -5)
  if [ -n "$deleted_tests" ]; then
    hold "$pr" "$hold_label" "deleted-tests" "Auto-merge skipped this PR because it deletes tests or test fixtures:
$(for f in $deleted_tests; do printf -- '- `%s`\n' "$f"; done)
Passing CI cannot show that removed tests were safe to remove, so a maintainer will review it."
    continue
  fi

  # Checks on the head commit, excluding this bot's own job.
  checks=$(gh api "repos/$repo/commits/$sha/check-runs?per_page=100" --paginate \
    --jq '.check_runs[] | select(.name != "automerge") | [.name, .status, (.conclusion // ""), (.completed_at // "")] | @tsv')
  if [ -z "$checks" ]; then
    log "  wait: no checks have run yet"; continue
  fi

  missing="" failed="" pending=""
  for name in $required; do
    # A required name also covers a matrix job's runs, which GitHub names
    # "<job> (<value>)", e.g. "test (ubuntu-latest)". Every such run must pass.
    runs=$(matching_checks "$name")
    if [ -z "$runs" ]; then missing="$missing $name"; continue; fi
    while IFS= read -r run; do
      # A check can run more than once (re-runs); use the latest attempt.
      line=$(awk -F'\t' -v n="$run" '$1 == n' <<<"$checks" | sort -t$'\t' -k4,4 | tail -1)
      IFS=$'\t' read -r _ status conclusion _ <<<"$line"
      if [ "$status" != completed ]; then pending="$pending $run"
      elif [ "$conclusion" != success ]; then failed="$failed $run"; fi
    done <<<"$runs"
  done
  others_bad=$(awk -F'\t' '$2 == "completed" && $3 != "success" && $3 != "neutral" && $3 != "skipped" { print $1 }' <<<"$checks" | sort -u | tr '\n' ' ')
  others_pending=$(awk -F'\t' '$2 != "completed" { print $1 }' <<<"$checks" | sort -u | tr '\n' ' ')

  if [ -n "$failed$others_bad" ]; then
    log "  skip: failing checks:$failed $others_bad"; continue
  fi
  if [ -n "$pending$others_pending$missing" ]; then
    log "  wait: pending or missing checks:$pending $others_pending$missing"; continue
  fi

  # Freshness: every required check must have finished after main last
  # moved, or it tested the PR against code that is no longer main.
  stale=""
  for name in $required; do
    while IFS= read -r run; do
      done_at=$(awk -F'\t' -v n="$run" '$1 == n { print $4 }' <<<"$checks" | sort | tail -1)
      if [[ "$done_at" < "$main_time" ]]; then stale="$stale $run"; fi
    done < <(matching_checks "$name")
  done
  if [ -n "$stale" ] && [ "$main_ci_ok" = true ] &&
     unaffected_by_main "$sha" "$(awk '{print $2}' <<<"$files")"; then
    log "  main moved, but not in this PR's directories; its checks still apply"
    stale=""
  fi
  if [ -n "$stale" ]; then
    hold "$pr" "$stale_label" "stale" "Main has moved since this PR's checks ran, so they no longer show it works with the current code. Please update your branch (the **Update branch** button, or merge or rebase \`main\`) to re-run CI; it will be merged automatically once the new checks pass."
    continue
  fi
  # The branch has been updated since it was marked stale; say so.
  if jq -e --arg l "$stale_label" '[.labels[].name] | index($l)' <<<"$row" >/dev/null; then
    [ "$dry_run" = true ] || gh pr edit "$pr" -R "$repo" --remove-label "$stale_label" >/dev/null 2>&1 || true
  fi

  state=$(gh pr view "$pr" -R "$repo" --json mergeable,mergeStateStatus --jq '"\(.mergeable) \(.mergeStateStatus)"')
  if [ "${state%% *}" != MERGEABLE ]; then
    log "  skip: not mergeable ($state)"; continue
  fi

  # Last gate: main's malware scanner over exactly the commit about to be
  # merged. The PR's own malware-scan check already ran, but this copy of the
  # rules comes from main and the PR cannot have changed it. Nothing from the
  # PR is executed; its commits are only read through git.
  if [ -f .github/scripts/malware-scan.sh ]; then
    git fetch -q --no-tags origin main || true
    if ! git fetch -q --no-tags origin "+refs/pull/$pr/head:refs/automerge/pr-$pr" ||
       ! git cat-file -e "$sha^{commit}" 2>/dev/null; then
      log "  wait: could not fetch $sha for the malware scan"; continue
    fi
    set +e
    scan=$(bash .github/scripts/malware-scan.sh "$main_sha" "$sha" .github/malware-scan-allow 2>&1)
    scan_rc=$?
    set -e
    if [ "$scan_rc" -ge 3 ]; then
      log "  wait: malware scan could not run (exit $scan_rc)"; continue
    fi
    if [ "$scan_rc" -ne 0 ]; then
      case "$scan_rc" in
        1) reason="malware-blocked"; what="patterns that indicate malware or code injection" ;;
        2) reason="security-review"; what="code a maintainer must review before it can merge (process execution, new dependencies, raw network access or similar)" ;;
      esac
      hold "$pr" "$hold_label" "$reason" "Auto-merge skipped this PR because the malware scan found $what:

\`\`\`text
$(grep -v '^::' <<<"$scan" | head -40)
\`\`\`
A maintainer will review it."
      continue
    fi
  fi

  # Substance: a PR that closes issues must change enough to plausibly do
  # what they ask. A one-line comment passes every test, so CI alone cannot
  # tell finished work from a placeholder that only claims an issue's points.
  # Count added lines that are not blank and not comment-only, in code and
  # config files; Markdown counts only towards documentation issues. Each
  # closed issue needs at least 5 (trivial), 20 (medium) or 50 (high) such
  # lines. Falling short holds the PR for a maintainer — it is not rejected.
  closes=$(gh pr view "$pr" -R "$repo" --json closingIssuesReferences \
    --jq '.closingIssuesReferences[].number' 2>/dev/null || true)
  if [ -n "$closes" ]; then
    need=0 docs=false claimed=""
    for issue in $closes; do
      labels=$(gh issue view "$issue" -R "$repo" --json labels --jq '[.labels[].name] | join(",")' 2>/dev/null || true)
      case ",$labels," in
        *",complexity: high,"*) need=$((need + 50)) ;;
        *",complexity: medium,"*) need=$((need + 20)) ;;
        *) need=$((need + 5)) ;;
      esac
      case ",$labels," in *",documentation,"*) docs=true ;; esac
      claimed="$claimed #$issue"
    done
    git fetch -q --no-tags origin "+refs/pull/$pr/head:refs/automerge/pr-$pr" 2>/dev/null || true
    have=$(git diff --no-ext-diff --no-textconv -U0 "$main_sha...$sha" 2>/dev/null | DOCS="$docs" perl -ne '
      if (m{^\+\+\+ (?:b/)?(.*)$}) { $f = $1; next }
      next if m{^\+\+\+|^---};
      next unless defined $f && s/^\+//;
      next if $f =~ m{(?:^|/)go\.sum$};
      my $prose = $f =~ /\.(?:md|txt|rst)$/i;
      next if $prose && $ENV{DOCS} ne "true";
      next if /^\s*$/;
      next if !$prose && m{^\s*(?://|#|/\*|\*|--)};
      $n++;
      END { print $n + 0 }')
    if [ "${have:-0}" -lt "$need" ]; then
      hold "$pr" "$hold_label" "insubstantial" "Auto-merge skipped this PR because its changes look too small for the issues it closes ($claimed): about ${have:-0} substantive added line(s), where those issues would normally need at least $need. Blank lines and comment-only lines do not count, and Markdown counts only towards documentation issues.

If the work is complete, a maintainer will review and merge it. Otherwise, please finish the issues before asking for review — points are awarded for the work the issue describes."
      continue
    fi
  fi

  if [ "$dry_run" = true ]; then
    log "  WOULD MERGE #$pr"
    merges=$((merges + 1))
    [ "$merges" -ge "$max_merges" ] && break
    continue
  fi

  # The squash message is the PR's own title and description. GitHub's
  # default folds in every intermediate commit message and its trailers
  # (maintainer merge commits, tool attributions), which then accumulate on
  # main as co-author credit for commits that were never the author's work.
  title=$(jq -r '.title' <<<"$row")
  body=$(gh pr view "$pr" -R "$repo" --json body --jq '.body // ""')

  # --match-head-commit refuses the merge if the author pushed after the
  # checks we just read, so we never merge untested code.
  if gh pr merge "$pr" -R "$repo" --squash --match-head-commit "$sha" \
      --subject "$title (#$pr)" --body "$body"; then
    log "  MERGED #$pr"
    gh pr edit "$pr" -R "$repo" --remove-label "$stale_label" >/dev/null 2>&1 || true
    # GitHub does not act on "Closes #N" when the merge is made with the
    # workflow token, so close the PR's linked issues here.
    for issue in $(gh pr view "$pr" -R "$repo" --json closingIssuesReferences \
        --jq '.closingIssuesReferences[].number'); do
      gh issue close "$issue" -R "$repo" --reason completed \
        --comment "Resolved by #$pr, which has been merged." >/dev/null 2>&1 \
        && log "  closed #$issue" || log "  could not close #$issue"
    done
    merges=$((merges + 1))
    # The next PR is judged against the main this merge produced.
    git fetch -q --no-tags origin main || true
    main_sha=$(git rev-parse origin/main)
    # Only checks that were fresh before any merge in this run are fresh now;
    # everything else must pass the directory test against the new main.
    main_time=$(date -u +%Y-%m-%dT%H:%M:%SZ)
    [ "$merges" -ge "$max_merges" ] && break
    continue
  fi
  log "  merge failed for #$pr"
done

if [ "$merges" -gt 0 ]; then
  log "merged $merges pull request(s)"
  # Pushes made with the workflow token do not trigger workflows, so run CI
  # on main explicitly to confirm the merged result.
  if [ "$dry_run" != true ] && [ -n "${CI_WORKFLOW:-}" ]; then
    gh workflow run "$CI_WORKFLOW" -R "$repo" --ref main || log "could not dispatch $CI_WORKFLOW on main"
  fi
else
  log "nothing to merge"
fi
