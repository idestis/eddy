#!/usr/bin/env bash
# Cut a release: one version for the hub, the agent and both charts.
#
#   hack/release.sh [X.Y.Z]   pick the version from commits (git-cliff), or pin it
#   DRY_RUN=1 hack/release.sh  show the changes and stop before committing
#
# It bumps both Chart.yaml files and the pinned versions in the install docs, writes
# CHANGELOG.md, commits "chore(release): vX.Y.Z", tags it and pushes main and the tag
# together. The Release workflow then builds, signs and publishes the images, the charts
# and the GitHub release. Normally run through `task release`.
set -euo pipefail

cd "$(dirname "$0")/.."

die() {
  echo "release: $*" >&2
  exit 1
}

# Files that pin the released version for copy-paste installs.
DOC_FILES=(
  deploy/charts/eddy-hub/values.yaml
  docs/install.md
  landing/src/routes/docs/install.tsx
  landing/src/routes/docs/sign-in.tsx
  landing/src/routes/docs/clusters.tsx
)

command -v git-cliff >/dev/null || die "git-cliff is not installed (brew install git-cliff)"
command -v gh >/dev/null || die "gh is not installed"
[[ -z $(git status --porcelain) ]] || die "the working tree is not clean"
[[ $(git rev-parse --abbrev-ref HEAD) == main ]] || die "release from main"
git fetch -q origin main --tags
[[ $(git rev-parse HEAD) == $(git rev-parse origin/main) ]] || die "main is not the same as origin/main; pull or push first"

current=$(sed -n 's/^version: //p' deploy/charts/eddy-hub/Chart.yaml)
if [[ -n ${1:-} ]]; then
  next=${1#v}
else
  next=$(git cliff --bumped-version 2>/dev/null | sed 's/^v//')
fi
[[ $next =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || die "could not pick a version from commits; pass one: task release -- 1.2.0"
tag="v$next"
if git rev-parse -q --verify "refs/tags/$tag" >/dev/null; then
  die "$tag already exists"
fi

# CI must be green on the commit being released.
sha=$(git rev-parse HEAD)
ci=$(gh run list --workflow CI --commit "$sha" --limit 1 --json status,conclusion -q '.[0] | "\(.status) \(.conclusion)"')
[[ $ci == "completed success" ]] || die "CI on $sha is '${ci:-not run}', not 'completed success'"

echo "release: $current -> $next ($tag)"

for chart in eddy-hub eddy-agent; do
  sed -i.bak -E "s/^version: .*/version: $next/; s/^appVersion: .*/appVersion: \"$next\"/" "deploy/charts/$chart/Chart.yaml"
  rm "deploy/charts/$chart/Chart.yaml.bak"
done
if [[ $current != "$next" ]]; then
  for f in "${DOC_FILES[@]}"; do
    OLD=$current NEW=$next perl -pi -e 's/(--version |eddy-(?:hub|agent):)\Q$ENV{OLD}\E(?![\w.])/$1$ENV{NEW}/g' "$f"
  done
fi
git cliff --tag "$tag" -o CHANGELOG.md

git --no-pager diff --stat
if [[ ${DRY_RUN:-0} == 1 ]]; then
  echo "release: DRY_RUN=1, stopping before the commit. Undo with: git checkout -- ."
  exit 0
fi

git add CHANGELOG.md deploy/charts/eddy-hub/Chart.yaml deploy/charts/eddy-agent/Chart.yaml "${DOC_FILES[@]}"
git commit -q -m "chore(release): $tag"
git tag -a "$tag" -m "Eddy $next"
git push --atomic origin main "$tag"
echo "release: pushed $tag. Follow the Release workflow with: gh run watch \$(gh run list --workflow Release --limit 1 --json databaseId -q '.[0].databaseId')"
