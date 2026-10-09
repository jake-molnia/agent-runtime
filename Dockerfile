# syntax=docker/dockerfile:1.7
FROM --platform=$BUILDPLATFORM golang:1.27.1-trixie@sha256:9baa6b4187bbb98d240372a8a235ac0bb6b5ddd52bba1431dc2f7c0705862728 AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags='-s -w' -o /out/agent-runtime ./cmd/agent-runtime \
    && CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags='-s -w' -o /out/sandboxd sigs.k8s.io/agent-sandbox/packages/sandboxd/cmd/sandboxd

FROM tailscale/tailscale:v1.102.4@sha256:2667499ed87ae29218f292556ba062918402dd5e92e93637af14867e4df12dd3 AS tailscale

FROM debian:trixie-slim@sha256:d7e12182ce18b85b93007c1dedf31f2d29e01ccf3182cc4017c709b6259bc132 AS desktop-tools
ARG TARGETARCH
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*
COPY desktop/download-aio.sh /usr/local/bin/download-aio
RUN sh /usr/local/bin/download-aio "${TARGETARCH}" /out

FROM --platform=$BUILDPLATFORM debian:trixie-slim@sha256:d7e12182ce18b85b93007c1dedf31f2d29e01ccf3182cc4017c709b6259bc132 AS skills
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates python3 \
    && rm -rf /var/lib/apt/lists/*
COPY skills.lock.json scripts/install-skills.py /tmp/skills/
RUN python3 /tmp/skills/install-skills.py --lock /tmp/skills/skills.lock.json

FROM debian:trixie-slim@sha256:d7e12182ce18b85b93007c1dedf31f2d29e01ccf3182cc4017c709b6259bc132 AS worker
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --uid 1000 --create-home worker
COPY --from=build /out/agent-runtime /usr/local/bin/agent-runtime
COPY harnesses/opencode.json /etc/agent-runtime/harnesses/opencode.json
COPY --from=skills /opt/agent-skill-bundles /opt/agent-skill-bundles
COPY --from=skills /opt/agent-skills /opt/agent-skills
USER 1000:1000
ENTRYPOINT ["agent-runtime"]
CMD ["worker"]

FROM node:26.9.0-trixie-slim@sha256:65f816afd401c1c4de3293acc46dce115398152af4bdcd73c103b096988922d7 AS sandbox
COPY desktop/install-tools.sh /tmp/install-desktop-tools.sh
RUN sh /tmp/install-desktop-tools.sh \
    && npm install --global --allow-scripts=@opencode/cli --no-audit --no-fund @opencode/cli@2.0.26 \
    && npm cache clean --force \
    && rm /tmp/install-desktop-tools.sh \
    && mkdir -p /workspace
COPY --from=build /out/agent-runtime /out/sandboxd /usr/local/bin/
COPY --from=tailscale /usr/local/bin/tailscale /usr/local/bin/tailscaled /usr/local/bin/
COPY --from=desktop-tools /out/aiod /out/computer-use /usr/local/bin/
COPY desktop/config/ /usr/local/share/agent-runtime/desktop/
COPY desktop/skills/ /usr/local/share/agent-runtime/skills/
COPY desktop/chromium-policy.json /etc/chromium/policies/managed/agent-runtime.json
COPY --from=skills /opt/agent-skill-bundles /opt/agent-skill-bundles
COPY --from=skills /opt/agent-skills /opt/agent-skills
COPY harnesses/opencode.json /etc/agent-runtime/harnesses/opencode.json
ENV HOME=/root USER=root LOGNAME=root SANDBOX_ROOT=/workspace DISPLAY=:99 \
    PATH="/opt/markitdown/bin:${PATH}" SANDBOX_CHROMIUM_SANDBOX=disabled \
    OPENCODE_CONFIG=/etc/agent-runtime/harnesses/opencode.json \
    OPENCODE_DISABLE_PROJECT_CONFIG=1
USER 0:0
WORKDIR /workspace
EXPOSE 8080 8081 4096 9090
ENTRYPOINT ["/usr/bin/tini", "-g", "--", "agent-runtime"]
CMD ["serve"]
