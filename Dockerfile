# syntax=docker/dockerfile:1.7
FROM golang:1.27.1-trixie@sha256:9baa6b4187bbb98d240372a8a235ac0bb6b5ddd52bba1431dc2f7c0705862728 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/agent-runtime ./cmd/agent-runtime \
    && CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/sandboxd sigs.k8s.io/agent-sandbox/packages/sandboxd/cmd/sandboxd

FROM tailscale/tailscale:v1.102.4@sha256:2667499ed87ae29218f292556ba062918402dd5e92e93637af14867e4df12dd3 AS tailscale

FROM debian:trixie-slim@sha256:d7e12182ce18b85b93007c1dedf31f2d29e01ccf3182cc4017c709b6259bc132 AS desktop-tools
ARG TARGETARCH
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl \
    && rm -rf /var/lib/apt/lists/*
COPY desktop/download-aio.sh /usr/local/bin/download-aio
RUN sh /usr/local/bin/download-aio "${TARGETARCH}" /out

FROM debian:trixie-slim@sha256:d7e12182ce18b85b93007c1dedf31f2d29e01ccf3182cc4017c709b6259bc132 AS worker
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && useradd --uid 1000 --create-home worker
COPY --from=build /out/agent-runtime /usr/local/bin/agent-runtime
USER 1000:1000
ENTRYPOINT ["agent-runtime"]
CMD ["worker"]

FROM node:26.9.0-trixie-slim@sha256:65f816afd401c1c4de3293acc46dce115398152af4bdcd73c103b096988922d7 AS sandbox
ARG OPENCODE_CHANNEL=2.0.26
RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates curl git python3 python3-pip python3-venv ripgrep tini tmux unzip file ffmpeg \
        xfce4-session xfce4-settings xfwm4 xfce4-panel xfdesktop4 xfconf thunar xfce4-terminal \
        dbus-x11 at-spi2-core tigervnc-standalone-server novnc websockify xauth x11-utils xdotool wmctrl scrot xclip \
        fonts-dejavu chromium desktop-file-utils \
    && rm -rf /var/lib/apt/lists/* \
    && npm install --global --prefix /opt/opencode-v2 --allow-scripts=@opencode/cli --no-audit --no-fund "@opencode/cli@${OPENCODE_CHANNEL}" \
    && ln -s /opt/opencode-v2/bin/opencode2 /usr/local/bin/opencode2 \
    && PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1 npm install --global --prefix /opt/playwright-mcp --ignore-scripts --no-audit --no-fund @playwright/mcp@0.0.83 \
    && ln -s /opt/playwright-mcp/bin/playwright-mcp /usr/local/bin/playwright-mcp \
    && PLAYWRIGHT_BROWSERS_PATH=/opt/playwright-browsers node /opt/playwright-mcp/lib/node_modules/@playwright/mcp/node_modules/playwright/cli.js install ffmpeg \
    && python3 -m venv /opt/markitdown \
    && /opt/markitdown/bin/pip install --no-cache-dir 'markitdown[all]==0.1.8' markitdown-mcp==0.0.1a7 \
    && ln -s /opt/markitdown/bin/markitdown /usr/local/bin/markitdown \
    && ln -s /opt/markitdown/bin/markitdown-mcp /usr/local/bin/markitdown-mcp \
    && mkdir -p /workspace /tmp/.X11-unix /tmp/.ICE-unix \
    && chmod 1777 /tmp/.X11-unix /tmp/.ICE-unix \
    && chown node:node /workspace
COPY --from=build /out/agent-runtime /out/sandboxd /usr/local/bin/
COPY --from=tailscale /usr/local/bin/tailscale /usr/local/bin/tailscaled /usr/local/bin/
COPY --from=desktop-tools /out/aiod /out/computer-use /usr/local/bin/
COPY desktop/config/ /usr/local/share/agent-runtime/desktop/
COPY desktop/skills/ /usr/local/share/agent-runtime/skills/
COPY desktop/chromium-policy.json /etc/chromium/policies/managed/agent-runtime.json
ENV HOME=/home/node SANDBOX_ROOT=/workspace DISPLAY=:99 PATH="/opt/markitdown/bin:${PATH}"
USER 1000:1000
WORKDIR /workspace
EXPOSE 8080 8081 4096 9090
ENTRYPOINT ["/usr/bin/tini", "-g", "--", "agent-runtime"]
CMD ["serve"]
