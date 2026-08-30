#!/usr/bin/env bash
# SPDX-FileCopyrightText: 2026 Damián Búho <damian.buho@proton.me>
#
# SPDX-License-Identifier: MIT
#
# Dogfood the FUSED `live` capability under nektos/act (capabilities-plan.md §1e):
# prove a build→live pipeline both RENDERS (pf-ci generate) and RUNS green on a real
# runner — buildx builds a NAMED OCI archive, the artifact crosses to the fused live
# job, the resolver-rendered run-steps `docker load` it TAGGED, `compose up --wait`
# brings it up, then `compose exec … test.d` gates. Asserts BOTH directions: test.d
# exit 0 => job green; test.d exit 1 => the failing run-step fails the job.
#
# `live` is NOT a ci-actions action: the fuse model dissolved it into plain ordered
# run-steps the RESOLVER emits (render.go `steps/fused`), so there is nothing to
# dispatch to. This is the cloud counterpart to render's TestLiveLeavesFuse (which
# pins the YAML wiring): here we exercise those rendered run-steps end to end against
# a live stack. container-build IS still an action (buildx), so the ci-actions repo
# is still mapped in below. Heavyweight by nature — needs act + a docker daemon +
# buildx + network — so it is NOT a unit test; it is gated m6e-only (the cloud runner
# has no act-in-act). Run it by hand:
#   dogfood/live.sh
#
# Seams it settles (all in capabilities-plan.md §1e):
#   - DooD: act mounts the HOST socket, so the stack persists after the job — the trap
#     tears it down (a real ephemeral runner needs no teardown; the GENERATED workflow
#     stays teardown-free). A distinctive compose project name avoids colliding with a
#     real stack.
#   - Artifact hand-off: --artifact-server-path gives act its v4 store; container-build
#     uploads, the live job downloads — the build→live edge, for real.
#   - Named image: container-build stamps `--output type=oci,name=<ref>`, so the load
#     is TAGGED and compose's `pull_policy: never` finds it (no skopeo, no anonymity).
set -euo pipefail

# ---------------------------------------------------------------------------------
# Config. REPO_CI_ACTIONS is mapped into act so the workflow uses the WORKING-TREE
# actions (not the published @v1) — the whole point of dogfooding local edits.
# ---------------------------------------------------------------------------------
HERE="$(cd "$(dirname "$0")" && pwd)"
REPO_CI="$(cd "${HERE}/.." && pwd)"
REPO_CI_ACTIONS="$(cd "${REPO_CI}/../ci-actions" && pwd)"
PFCI="${PFCI:-${REPO_CI}/dist/pf-ci}"
ACT_IMAGE="${ACT_IMAGE:-catthehacker/ubuntu:act-latest}"
REGISTRY="${REGISTRY:-dogfood.local}"           # passed as --var, now a harmless no-op
COMPOSE_PROJECT="pf-live-dogfood"               # distinctive => no real-stack collision
# The BUILT image renders BARE (no registry prefix): the fixture declares no per-tool
# `registry:`, so composeImage leaves it `<basename>:<tag>` — it is loaded from the OCI
# tar and never pulled, so it needs no registry. (Was `${REGISTRY}/…` before the
# per-tool-registry refactor made the built image registry-free.)
IMAGE_REF="live-fixture:dev"                    # composed + stamped; :dev pinned via the --var below

WORK="$(mktemp -d)"
ARTIFACTS="$(mktemp -d)"

# log: one line, prefixed, so the run reads as a sequence of decisions.
log() { echo "dogfood-live: $*"; }

# cleanup: tear the DooD stack + loaded image down (seam 1) and drop the temp trees.
# Always runs (trap) so a mid-run failure never leaks a stack onto the host daemon.
cleanup() {
  local rc=$?
  log "cleanup (rc=${rc}): compose down + rmi + temp dirs"
  docker compose -p "${COMPOSE_PROJECT}" down --volumes >/dev/null 2>&1 || true
  docker rmi --force "${IMAGE_REF}" >/dev/null 2>&1 || true
  rm -rf "${WORK}" "${ARTIFACTS}" 2>/dev/null || true
  return "${rc}"
}
trap cleanup EXIT

# preflight: graceful, explicit skip when the heavyweight deps are absent (this is a
# local harness, not CI — a missing tool is a SKIP with a reason, not a hard failure).
preflight() {
  local missing=()
  command -v act >/dev/null 2>&1 || missing+=(act)
  command -v docker >/dev/null 2>&1 || missing+=(docker)
  docker buildx version >/dev/null 2>&1 || missing+=(buildx)
  [ -x "${PFCI}" ] || { log "SKIP: pf-ci not built at ${PFCI} (run: make build-local)"; exit 0; }
  if [ "${#missing[@]}" -gt 0 ]; then
    log "SKIP: missing ${missing[*]} — the live dogfood needs act + docker + buildx"
    exit 0
  fi
  docker info >/dev/null 2>&1 || { log "SKIP: no reachable docker daemon"; exit 0; }
}

# fixture: a minimal build→live project. The image bakes the gating test.d ($1 is its
# exit code) on PATH; the compose service references the loaded tag DIRECTLY (the fused
# run-steps `docker load` the stamped tar — there is no ref-export step to interpolate)
# and never pulls. The service is named `app` to match the `compose exec … app` run.
# The compose file is the default-discovery `compose.yaml` at the repo root, because the
# rendered run-steps are BARE `docker compose` (faithful to the real m6e dc-up-d/container-
# test recipes). A real project keeping compose under `.compose/` relies on COMPOSE_FILE,
# which the fused forge job does NOT yet inject — a known §8 parity gap, out of scope here.
write_fixture() {
  local test_exit="$1"
  mkdir -p "${WORK}/.github/workflows"

  cat > "${WORK}/test.d" <<EOF
#!/bin/sh
echo "test.d: running gating checks (will exit ${test_exit})"
exit ${test_exit}
EOF
  chmod +x "${WORK}/test.d"

  cat > "${WORK}/Dockerfile" <<'EOF'
FROM busybox
ARG B19_UBUNTU_SERIES=unset
RUN echo "built for series=${B19_UBUNTU_SERIES}" > /built-series
COPY test.d /usr/local/bin/test.d
CMD ["sleep", "infinity"]
EOF

  cat > "${WORK}/compose.yaml" <<EOF
name: ${COMPOSE_PROJECT}
services:
  app:
    image: ${IMAGE_REF}
    pull_policy: never
EOF

  cat > "${WORK}/projectfile.yaml" <<'EOF'
---
$schema: https://projectfile.org/schema/v1.json
identity:
  name: live-fixture
  namespace: org.projectfile.dogfood.live-fixture
  title: {en: live dogfood fixture}
kind: SoftwareSourceCode
license: {spdx: MIT}
org:
  projectfile:
    ci:
      image: live-fixture
      matrix:
        axes:
          B19_UBUNTU_SERIES: [resolute]
      tools:
        container-build: {action: container-build}
        dc-up-d:         {fuse: live, run: docker compose up -d --wait}
        container-test:  {fuse: live, run: docker compose exec -T app test.d}
      nodes:
        image-built:      {matrix: true, needs: {container-build: true}}
        container-ready:  {matrix: true, needs: {image-built: true, dc-up-d: true}}
        container-tested: {matrix: true, needs: {container-ready: true, container-test: true}}
        ready-to-publish: {goal: true, needs: {container-tested: true}}
EOF

  "${PFCI}" generate -target gha -pf "${WORK}/projectfile.yaml" -o "${WORK}/.github/workflows/ci.yaml"
  ( cd "${WORK}" && git init -q . && git add -A     \
      && git -c user.email=dogfood@local -c user.name=dogfood commit -qm fixture )
}

# run_act: one act invocation over the fixture workflow, log to $1. DooD socket is
# act's default; the local-repository map points ci-actions@v1 at the working tree.
run_act() {
  local logfile="$1"
  rm -rf "${ARTIFACTS:?}"/* 2>/dev/null || true
  docker compose -p "${COMPOSE_PROJECT}" down --volumes >/dev/null 2>&1 || true
  docker rmi --force "${IMAGE_REF}" >/dev/null 2>&1 || true
  ( cd "${WORK}" && act push                                                \
      -W .github/workflows/ci.yaml                                           \
      -P "ubuntu-latest=${ACT_IMAGE}"                                       \
      --var "DESTINATION_DOCKER_REGISTRY=${REGISTRY}"                       \
      --var "M6E_BASE_IMAGE_DEFAULT_VERSION=dev"                            \
      --pull=false                                                          \
      --artifact-server-path "${ARTIFACTS}"                                 \
      --local-repository "projectfile/ci-actions@v1=${REPO_CI_ACTIONS}"     \
      > "${logfile}" 2>&1 ) || true   # job-failure is an EXPECTED outcome we assert on
}

# assert_contains / assert_absent: grep-based gates over a run log, with the matched
# context echoed so a failure is self-explanatory.
assert_contains() {
  local logfile="$1" needle="$2" why="$3"
  if ! grep -qF "${needle}" "${logfile}"; then
    log "FAIL: ${why} — expected ${needle@Q} in ${logfile}"
    exit 1
  fi
  log "ok: ${why}"
}
assert_absent() {
  local logfile="$1" needle="$2" why="$3"
  if grep -qF "${needle}" "${logfile}"; then
    log "FAIL: ${why} — unexpected ${needle@Q} in ${logfile}"
    exit 1
  fi
  log "ok: ${why}"
}

main() {
  preflight
  log "fixture+artifacts: ${WORK} ${ARTIFACTS}; image ref ${IMAGE_REF}"

  # --- POSITIVE: gating test.d exits 0 => the whole pipeline is green. ---
  # The fused job has NO `live:` action lines anymore — it is the resolver-rendered
  # run-steps: `docker load` the stamped tar, `compose up --wait`, then `compose exec
  # … test.d`. We assert the load tag, that the gating test.d actually RAN against the
  # live container (proves load→up→exec all succeeded), and that the job went green.
  log "=== POSITIVE run (test.d exit 0) ==="
  write_fixture 0
  run_act "${WORK}/act-positive.log"
  assert_contains "${WORK}/act-positive.log" "Loaded image: ${IMAGE_REF}"     \
    "named OCI archive loads TAGGED (no anonymous image)"
  assert_contains "${WORK}/act-positive.log" "test.d: running gating checks (will exit 0)"      \
    "gating test.d ran against the live container (load → up → exec)"
  assert_contains "${WORK}/act-positive.log" "Job succeeded" "the green run finishes the job"
  assert_absent   "${WORK}/act-positive.log" "Job failed" "no job failed on the green run"
  assert_absent   "${WORK}/act-positive.log" "ci-actions/live" "live is run-steps, not an action ref"
  # Fusion: the live region is ONE job (container-test); dc-up-d never stands alone.
  assert_absent   "${WORK}/act-positive.log" "[ci/dc-up-d" "dc-up-d is ABSORBED (fusion), not a standalone job"

  # --- NEGATIVE: gating test.d exits 1 => the `compose exec … test.d` run-step exits
  # non-zero, so the fused job fails (no action to swallow it — `set -e` on the step). ---
  log "=== NEGATIVE run (test.d exit 1) ==="
  write_fixture 1
  run_act "${WORK}/act-negative.log"
  assert_contains "${WORK}/act-negative.log" "test.d: running gating checks (will exit 1)"      \
    "the gating test.d run-step executed before failing"
  assert_contains "${WORK}/act-negative.log" "Job failed" "a failing gate fails the job"

  log "PASS: live renders AS run-steps AND runs green under act (positive + negative)"
}

main "$@"
