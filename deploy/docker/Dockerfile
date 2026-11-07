# syntax=docker/dockerfile:1
#
# Damson images (ARCHITECTURE section 16). One build, four targets:
#
#   damson              the controller (and any role) image: one static binary
#   damson-agent        agent + git, ssh, ansible-core (ansible plugin)
#   damson-agent-build  agent + git, buildah, helm (buildah and helm plugins)
#
#   docker build -f deploy/docker/Dockerfile --target damson-agent -t damson-agent .
#
# Pinned tool versions are build arguments. The helm archive is verified against
# a sha256 (amd64 and arm64 below); change both when you change HELM_VERSION.

ARG GO_VERSION=1.27.3
ARG NODE_VERSION=22

# ---- the web UI, embedded in the binary
FROM node:${NODE_VERSION}-bookworm-slim AS web
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

# ---- the binary
FROM golang:${GO_VERSION}-bookworm AS build
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /src/web/build ./web/build
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w -X github.com/denyszorinets/damson/internal/version.Version=${VERSION}" \
      -o /out/damson ./cmd/damson

# ---- controller image
FROM gcr.io/distroless/static-debian12:nonroot AS damson
COPY --from=build /out/damson /usr/local/bin/damson
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/damson"]

# ---- agent base: git and ssh, a non-root user, the binary
FROM debian:trixie-slim AS agent-base
RUN apt-get update \
 && apt-get install -y --no-install-recommends ca-certificates git openssh-client tini \
 && rm -rf /var/lib/apt/lists/* \
 && useradd --create-home --uid 10001 --shell /usr/sbin/nologin damson \
 && echo "damson:100000:65536" > /etc/subuid \
 && echo "damson:100000:65536" > /etc/subgid
COPY --from=build /out/damson /usr/local/bin/damson
USER damson
WORKDIR /home/damson
ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/damson"]
CMD ["agent"]

# ---- agent for the ansible plugin
FROM agent-base AS damson-agent
ARG ANSIBLE_CORE_VERSION=2.18.1
USER root
RUN apt-get update \
 && apt-get install -y --no-install-recommends python3 python3-venv sshpass \
 && rm -rf /var/lib/apt/lists/* \
 && python3 -m venv /opt/ansible \
 && /opt/ansible/bin/pip install --no-cache-dir "ansible-core==${ANSIBLE_CORE_VERSION}" pywinrm \
 && /opt/ansible/bin/ansible-galaxy collection install --collections-path /opt/ansible-collections ansible.posix community.general \
 && ln -s /opt/ansible/bin/ansible /opt/ansible/bin/ansible-playbook /opt/ansible/bin/ansible-galaxy /usr/local/bin/
ENV ANSIBLE_COLLECTIONS_PATH=/opt/ansible-collections
USER damson

# ---- agent for the buildah and helm plugins
FROM agent-base AS damson-agent-build
ARG HELM_VERSION=3.16.4
ARG HELM_SHA256_AMD64=fc307327959aa38ed8f9f7e66d45492bb022a66c3e5da6063958254b9767d179
ARG HELM_SHA256_ARM64=d3f8f15b3d9ec8c8678fbf3280c3e5902efabe5912e2f9fcf29107efbc8ead69
ARG TARGETARCH
USER root
RUN apt-get update \
 && apt-get install -y --no-install-recommends buildah fuse-overlayfs uidmap curl \
 && case "${TARGETARCH:-amd64}" in \
      amd64) sum="${HELM_SHA256_AMD64}" ;; \
      arm64) sum="${HELM_SHA256_ARM64}" ;; \
      *) echo "unsupported architecture ${TARGETARCH}" >&2; exit 1 ;; \
    esac \
 && curl -fsSL -o /tmp/helm.tgz "https://get.helm.sh/helm-v${HELM_VERSION}-linux-${TARGETARCH:-amd64}.tar.gz" \
 && echo "${sum}  /tmp/helm.tgz" | sha256sum -c - \
 && tar -xzf /tmp/helm.tgz -C /usr/local/bin --strip-components=1 "linux-${TARGETARCH:-amd64}/helm" \
 && rm /tmp/helm.tgz \
 && apt-get purge -y curl && apt-get autoremove -y && rm -rf /var/lib/apt/lists/*
# Rootless buildah without /dev/fuse: the vfs driver and chroot isolation work in
# an unprivileged pod. Override STORAGE_DRIVER=overlay (with fuse-overlayfs) for
# speed when the pod has /dev/fuse.
ENV STORAGE_DRIVER=vfs BUILDAH_ISOLATION=chroot
USER damson
