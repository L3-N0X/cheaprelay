# syntax=docker/dockerfile:1

FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
COPY web ./web
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/srt-twitch-relay .

FROM alpine:3.22
RUN apk add --no-cache ffmpeg \
    && adduser -D -H -u 10001 relay \
    && mkdir /data && chown relay /data
COPY --from=build /out/srt-twitch-relay /usr/local/bin/srt-twitch-relay
COPY --chmod=755 scripts/make-fallback.sh /usr/local/bin/make-fallback
USER relay
# settings changed in the web UI are saved here (see docker-compose.yml)
VOLUME /data
EXPOSE 9000/udp 8080/tcp
HEALTHCHECK --interval=30s --timeout=3s --start-period=10s \
    CMD wget -qO /dev/null http://127.0.0.1:8080/healthz || exit 1
ENTRYPOINT ["srt-twitch-relay"]
CMD ["-config", "/config/config.toml"]
