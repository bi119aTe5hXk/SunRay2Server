# syntax=docker/dockerfile:1

FROM golang:1.23-bookworm AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/sunrayd ./cmd/sunrayd

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates \
    freerdp2-x11 \
    x11vnc \
    xauth \
    xvfb \
    && install -d -o root -g root -m 1777 /tmp/.X11-unix \
    && rm -rf /var/lib/apt/lists/*
COPY --from=build /out/sunrayd /usr/local/bin/sunrayd

USER 65534:65534
EXPOSE 7009/tcp
ENTRYPOINT ["/usr/local/bin/sunrayd"]
CMD ["-config", "/etc/sunray/config.yaml"]
