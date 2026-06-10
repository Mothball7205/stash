# This dockerfile should be built with `make docker-build` from the stash root.

# Build Frontend
FROM node:24-alpine AS frontend
RUN apk add --no-cache make git
## cache node_modules separately
COPY ./ui/v2.5/package.json ./ui/v2.5/pnpm-lock.yaml /stash/ui/v2.5/
WORKDIR /stash
COPY Makefile /stash/
COPY ./graphql /stash/graphql/
COPY ./ui /stash/ui/
# pnpm install with npm
RUN npm install -g pnpm
RUN make pre-ui
RUN make generate-ui
ARG GITHASH
ARG STASH_VERSION
RUN BUILD_DATE=$(date +"%Y-%m-%d %H:%M:%S") make ui-only

# Build Backend
# debian-based builder: the binary is CGO and must link against glibc to run
# in the debian runtime image below
FROM golang:1.25.9-trixie AS backend
WORKDIR /stash
COPY ./go* ./*.go Makefile gqlgen.yml .gqlgenc.yml /stash/
COPY ./graphql /stash/graphql/
COPY ./scripts /stash/scripts/
COPY ./pkg /stash/pkg/
COPY ./cmd /stash/cmd/
COPY ./internal /stash/internal/
# needed for generate-login-locale
COPY ./ui /stash/ui/
RUN make generate-backend generate-login-locale
COPY --from=frontend /stash /stash/
ARG GITHASH
ARG STASH_VERSION
RUN make flags-release flags-pie stash

# Final Runnable Image
FROM debian:trixie-slim
# Debian's ffmpeg is built with VAAPI, QSV (libvpl) and NVENC support.
# - VAAPI/QSV need the va driver packages below (intel-media-va-driver-non-free
#   requires the non-free component, hence the sources edit)
# - NVENC needs no packages here: the NVIDIA container toolkit injects the
#   driver libraries at runtime
RUN sed -i 's/Components: main/Components: main contrib non-free non-free-firmware/' /etc/apt/sources.list.d/debian.sources \
    && apt-get update \
    && apt-get install -y --no-install-recommends \
        ca-certificates tzdata gosu \
        ffmpeg libva2 libva-drm2 vainfo mesa-va-drivers \
        intel-media-va-driver-non-free i965-va-driver libvpl2 libmfx-gen1.2 \
        libvips-tools \
        python3 python3-pip \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/*
COPY docker/entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh
COPY --from=backend /stash/stash /usr/bin/
ENV STASH_CONFIG_FILE=/config/config.yml
# include the video capability so NVENC works when run with --gpus
ENV NVIDIA_DRIVER_CAPABILITIES=compute,video,utility
EXPOSE 9999
ENTRYPOINT ["/entrypoint.sh", "stash"]
