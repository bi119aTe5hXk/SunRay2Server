# syntax=docker/dockerfile:1

FROM golang:1.23-bookworm AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/sunrayd ./cmd/sunrayd

FROM debian:bookworm-slim
COPY --from=build /out/sunrayd /usr/local/bin/sunrayd

USER 65534:65534
EXPOSE 7009/tcp
ENTRYPOINT ["/usr/local/bin/sunrayd"]
CMD ["-config", "/etc/sunray/config.yaml"]
