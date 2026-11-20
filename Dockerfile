FROM --platform=$BUILDPLATFORM golang:1.26-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY *.go ./
COPY internal ./internal
COPY web ./web
ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -trimpath -ldflags="-s -w" -o /out/yt-downloader .

FROM denoland/deno:bin-2.9.8@sha256:b507583d3c68ba69f3ed2ce5969ae5b8b70f43421c781279c7aea06435263543 AS deno

# These upstream standalone binaries include the Python runtime and yt-dlp-ejs.
FROM scratch AS ytdlp-amd64
ADD --checksum=sha256:767ba9dce7052aa196f8b12652f433d76de128ba168966fa84b6394d5e6c6795 --chmod=755 https://github.com/yt-dlp/yt-dlp/releases/download/2026.10.26/yt-dlp_linux /yt-dlp
FROM scratch AS ytdlp-arm64
ADD --checksum=sha256:e87e9533e89fe5889abaa21f5114dfe7756d6aa6219b83551add04d48b18e483 --chmod=755 https://github.com/yt-dlp/yt-dlp/releases/download/2026.10.26/yt-dlp_linux_aarch64 /yt-dlp
FROM ytdlp-${TARGETARCH} AS ytdlp

FROM debian:bookworm-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates ffmpeg \
    && rm -rf /var/lib/apt/lists/* \
    && groupadd --gid 10001 ytd \
    && useradd --uid 10001 --gid 10001 --home-dir /data --no-create-home --no-log-init --shell /usr/sbin/nologin ytd \
    && install -d -m 0700 -o ytd -g ytd /data \
    && install -d -m 0755 -o ytd -g ytd /downloads
COPY --from=ytdlp /yt-dlp /usr/local/bin/yt-dlp
COPY --from=deno /deno /usr/local/bin/deno
COPY --from=build /out/yt-downloader /usr/local/bin/yt-downloader
ENV HOME=/data \
    YTDL_DATA_DIR=/data \
    YTDL_DOWNLOAD_ROOT=/downloads \
    YTDL_LISTEN=0.0.0.0:8080
USER 10001:10001
RUN yt-dlp --version && deno --version && ffprobe -version > /dev/null
VOLUME ["/data", "/downloads"]
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/yt-downloader"]
CMD ["-web"]
