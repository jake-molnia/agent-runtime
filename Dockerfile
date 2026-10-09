# syntax=docker/dockerfile:1.7
FROM golang:1.27.1-trixie@sha256:9baa6b4187bbb98d240372a8a235ac0bb6b5ddd52bba1431dc2f7c0705862728 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/agent-runtime ./cmd/agent-runtime \
    && CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/sandboxd sigs.k8s.io/agent-sandbox/packages/sandboxd/cmd/sandboxd

FROM tailscale/tailscale:v1.102.4@sha256:2667499ed87ae29218f292556ba062918402dd5e92e93637af14867e4df12dd3 AS tailscale

FROM debian:trixie-slim@sha256:d7e12182ce18b85b93007c1dedf31f2d29e01ccf3182cc4017c709b6259bc132 AS worker
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --uid 1000 --create-home worker
COPY --from=build /out/agent-runtime /usr/local/bin/agent-runtime
USER 1000:1000
ENTRYPOINT ["agent-runtime"]
CMD ["worker"]

FROM node:26.9.0-trixie-slim@sha256:65f816afd401c1c4de3293acc46dce115398152af4bdcd73c103b096988922d7 AS sandbox
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates git python3 ripgrep tini \
    && rm -rf /var/lib/apt/lists/* \
    && npm install --global --allow-scripts=@opencode/cli --no-audit --no-fund @opencode/cli@2.0.26 \
    && npm cache clean --force \
    && mkdir /workspace && chown node:node /workspace
COPY --from=build /out/agent-runtime /out/sandboxd /usr/local/bin/
COPY --from=tailscale /usr/local/bin/tailscale /usr/local/bin/tailscaled /usr/local/bin/
ENV HOME=/home/node SANDBOX_ROOT=/workspace
USER 1000:1000
WORKDIR /workspace
EXPOSE 8080 8081 4096 9090
ENTRYPOINT ["/usr/bin/tini", "-g", "--", "agent-runtime"]
CMD ["serve"]
