# SPDX-FileCopyrightText: 2026 Damián Búho <damian.buho@proton.me>
#
# SPDX-License-Identifier: MIT

ARG B19_GO_BASE_IMAGE=registry.invalid/b19/go:latest
ARG B19_UBUNTU_BASE_IMAGE=registry.invalid/b19/ubuntu/resolute:latest
ARG B19_UBUNTU_SERIES=resolute

FROM ${B19_GO_BASE_IMAGE} AS projectfile-ci-resolver-builder

ARG B19_COLOR
ARG B19_FETCH_DOCKER_CACHE
ARG B19_FETCH_LOCAL_CACHE
ARG B19_OFFGRID_MODE
ARG B19_VERBOSITY
# In-container `go mod download` resolves the pinned kiota.ch/projectfile/core
# from the Forgejo Go registry; public modules 404 there and fall through to
# direct. The forge may override via vars.GOPROXY.
ARG GOPROXY=https://kiota.ch/api/packages/projectfile/go,direct
ARG LANG=""
ARG M6E_AI=N
ARG M6E_APT_CACHE_HOST=""
ARG M6E_APT_CACHE_PORT=""
ARG M6E_BUILD_DEBUG=""
ARG M6E_NAMESPACE
ARG M6E_NEAR_CACHE_HOST=""
ARG M6E_PROJECT
ARG M6E_VERSION
ARG TARGETARCH

# Export GOPROXY into the build-stage environment (ARG alone is not visible to go).
ENV GOPROXY=${GOPROXY}

COPY --chown=${B19_UID}:${B19_GID} .container/compile-go/           /
COPY --chown=${B19_UID}:${B19_GID} main.go go.mod go.sum            ${B19_HOME}/
COPY --chown=${B19_UID}:${B19_GID} internal/                        ${B19_HOME}/internal/

USER 0

WORKDIR ${B19_HOME}

RUN --mount=type=bind,from=fetch,source=.,target=/fetch                                           \
    --mount=type=cache,target=${B19_DOWNLOAD_PATH},sharing=shared                                 \
    --mount=type=cache,target=${GOCACHE},sharing=locked                                           \
    --mount=type=cache,target=${GOMODCACHE},sharing=locked                                        \
    --mount=type=cache,id=apt-cache-${B19_UBUNTU_SERIES}-${TARGETARCH},target=/var/cache/apt,sharing=shared     \
    --mount=type=cache,id=apt-lists-${B19_UBUNTU_SERIES}-${TARGETARCH},target=/var/lib/apt,sharing=shared       \
    --mount=type=tmpfs,target=${B19_TEMP_PATH}                                                    \
    build-stage compile-go

# hadolint DL3002
# hadolint ignore=DL3066 # B19_UID comes from the root
USER ${B19_UID}

FROM ${B19_UBUNTU_BASE_IMAGE} AS projectfile-ci-resolver

ARG B19_COLOR
ARG B19_FETCH_DOCKER_CACHE
ARG B19_FETCH_LOCAL_CACHE
ARG B19_OFFGRID_MODE
ARG B19_VERBOSITY
ARG LANG=""
ARG M6E_AI=N
ARG M6E_APT_CACHE_HOST=""
ARG M6E_APT_CACHE_PORT=""
ARG M6E_BUILD_DEBUG=""
ARG M6E_NAMESPACE
ARG M6E_NEAR_CACHE_HOST=""
ARG M6E_PROJECT
ARG M6E_VERSION
ARG TARGETARCH

ENV M6E_VERSION=${M6E_VERSION}

COPY --chown=${B19_UID}:${B19_GID} .container/base/ /
COPY --from=projectfile-ci-resolver-builder /export /

USER 0

WORKDIR ${B19_HOME}

RUN --mount=type=bind,from=fetch,source=.,target=/fetch                                           \
    --mount=type=cache,target=${B19_DOWNLOAD_PATH},sharing=shared                                 \
    --mount=type=cache,id=apt-cache-${B19_UBUNTU_SERIES}-${TARGETARCH},target=/var/cache/apt,sharing=shared     \
    --mount=type=cache,id=apt-lists-${B19_UBUNTU_SERIES}-${TARGETARCH},target=/var/lib/apt,sharing=shared       \
    --mount=type=tmpfs,target=${B19_TEMP_PATH}                                                    \
    build-stage base

# hadolint ignore=DL3066 # B19_UID comes from the root
USER ${B19_UID}

COPY --chown=${B19_UID}:${B19_GID} .container/user/ /

RUN --mount=type=bind,from=fetch,source=.,target=/fetch                                             \
    --mount=type=cache,target=${B19_DOWNLOAD_PATH},sharing=shared,uid=${B19_UID},gid=${B19_GID}     \
    --mount=type=tmpfs,target=${B19_TEMP_PATH}                                                      \
    build-stage user

# ENTRYPOINT ["entrypoint.d"] is inherited
# HEALTHCHECK CMD ["healthcheck.d"] is inherited
# CMD ["sleep", "infinity"]
