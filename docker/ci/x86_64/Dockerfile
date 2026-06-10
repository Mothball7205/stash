FROM --platform=$BUILDPLATFORM alpine:latest AS binary
ARG TARGETPLATFORM
WORKDIR /
COPY stash-*  /
RUN if [ "$TARGETPLATFORM" = "linux/arm/v7" ];   then BIN=stash-linux-arm32v7; \
    elif [ "$TARGETPLATFORM" = "linux/arm64" ];  then BIN=stash-linux-arm64v8; \
    elif [ "$TARGETPLATFORM" = "linux/amd64" ];  then BIN=stash-linux; \
    fi; \
    mv $BIN /stash

FROM debian:trixie-slim AS app
COPY --from=binary /stash /usr/bin/

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
        libvips-tools \
        python3 python3-pip python3-requests python3-requests-toolbelt python3-lxml \
    && if [ "$(dpkg --print-architecture)" = "amd64" ]; then \
        apt-get install -y --no-install-recommends \
            intel-media-va-driver-non-free i965-va-driver libvpl2 libmfx-gen1.2; \
    fi \
    && pip install --no-cache-dir --break-system-packages mechanicalsoup cloudscraper stashapp-tools \
    && apt-get clean \
    && rm -rf /var/lib/apt/lists/*

COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh
ENV STASH_CONFIG_FILE=/config/config.yml
# include the video capability so NVENC works when run with --gpus
ENV NVIDIA_DRIVER_CAPABILITIES=compute,video,utility
# runtime pip installs go to the /config mount so user-installed python
# dependencies persist across image updates
ENV PIP_TARGET=/config/.python-packages \
    PYTHONPATH=/config/.python-packages

# Basic build-time metadata as defined at https://github.com/opencontainers/image-spec/blob/main/annotations.md#pre-defined-annotation-keys
LABEL org.opencontainers.image.title="Stash" \
    org.opencontainers.image.description="An organizer for your porn, written in Go." \
    org.opencontainers.image.url="https://stashapp.cc" \
    org.opencontainers.image.documentation="https://docs.stashapp.cc" \
    org.opencontainers.image.source="https://github.com/stashapp/stash" \
    org.opencontainers.image.licenses="AGPL-3.0"

EXPOSE 9999
ENTRYPOINT ["/entrypoint.sh", "stash"]
