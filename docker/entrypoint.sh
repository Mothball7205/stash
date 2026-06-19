#!/bin/sh
set -e

# Default PUID/PGID if not specified
PUID=${PUID:-0}
PGID=${PGID:-0}

# Migrate legacy data from /root/.stash if applicable
migrate_legacy_config() {
    if [ -d /root/.stash ] && [ ! -f /config/config.yml ]; then
        echo "Migrating legacy config from /root/.stash to /config..."
        mkdir -p /config
        cp -rp /root/.stash/* /config/ 2>/dev/null || true
    fi
}

# Install Python dependencies if requirements.txt exists.
# Packages are installed into $PIP_TARGET (on the /config mount) so they
# persist across image updates, and a checksum marker skips the reinstall
# when requirements.txt hasn't changed since the last start.
install_python_deps() {
    if [ -f /config/requirements.txt ]; then
        PIP_TARGET=${PIP_TARGET:-/config/.python-packages}
        export PIP_TARGET
        marker="$PIP_TARGET/.requirements.sha256"
        checksum=$(sha256sum /config/requirements.txt | cut -d' ' -f1)
        if [ -f "$marker" ] && [ "$(cat "$marker")" = "$checksum" ]; then
            return
        fi
        echo "Installing Python dependencies from /config/requirements.txt..."
        mkdir -p "$PIP_TARGET"
        # --upgrade is needed for pip to replace files already in the target dir
        # --break-system-packages is required on debian (PEP 668) and ignored where unsupported
        if pip3 install --no-cache-dir --upgrade --break-system-packages -r /config/requirements.txt 2>/dev/null \
            || pip3 install --no-cache-dir --upgrade -r /config/requirements.txt 2>/dev/null; then
            echo "$checksum" > "$marker"
        else
            echo "Warning: failed to install Python dependencies from /config/requirements.txt"
        fi
    fi
}

# Not running as root: just run the application directly
if [ "$(id -u)" != "0" ]; then
    exec "$@"
fi

migrate_legacy_config
install_python_deps

# Running as root without PUID/PGID: run as root directly
if [ "$PUID" -eq 0 ] || [ "$PGID" -eq 0 ]; then
    exec "$@"
fi

# Handle rootless/USER mapping
echo "Configuring user stash with PUID=$PUID and PGID=$PGID..."

# Create group if it doesn't exist
if ! getent group "$PGID" >/dev/null 2>&1 && ! getent group stash >/dev/null 2>&1; then
    groupadd -g "$PGID" stash 2>/dev/null || addgroup -g "$PGID" stash 2>/dev/null
fi

# Create user if it doesn't exist
if ! getent passwd "$PUID" >/dev/null 2>&1 && ! getent passwd stash >/dev/null 2>&1; then
    useradd -u "$PUID" -g "$PGID" -m -s /bin/sh stash 2>/dev/null || adduser -u "$PUID" -G stash -D -s /bin/sh stash 2>/dev/null
fi

# Ensure stash user is named properly in passwd
STASH_USER=$(getent passwd "$PUID" | cut -d: -f1)

# Function to add user to group by GID
add_user_to_host_gid() {
    gid="$1"
    group_name="$2"
    if [ -n "$gid" ] && [ "$gid" -gt 0 ]; then
        existing_group=$(getent group "$gid" | cut -d: -f1)
        if [ -n "$existing_group" ]; then
            usermod -aG "$existing_group" "$STASH_USER" 2>/dev/null || addgroup "$STASH_USER" "$existing_group" 2>/dev/null
        else
            groupadd -g "$gid" "$group_name" 2>/dev/null || addgroup -g "$gid" "$group_name" 2>/dev/null
            usermod -aG "$group_name" "$STASH_USER" 2>/dev/null || addgroup "$STASH_USER" "$group_name" 2>/dev/null
        fi
    fi
}

# Map GPU devices
if [ -d /dev/dri ]; then
    for dev in /dev/dri/renderD* /dev/dri/card*; do
        if [ -e "$dev" ]; then
            dev_gid=$(stat -c '%g' "$dev" 2>/dev/null)
            add_user_to_host_gid "$dev_gid" "host_dri_$(basename "$dev")"
        fi
    done
fi

for dev in /dev/nvidia*; do
    if [ -e "$dev" ]; then
        dev_gid=$(stat -c '%g' "$dev" 2>/dev/null)
        add_user_to_host_gid "$dev_gid" "host_nvidia_$(basename "$dev")"
    fi
done

# Adjust writeable mounts ownership if owned by root
if [ "$SKIP_CHOWN" != "true" ]; then
    # /config is always chowned to ensure config files are writeable
    mkdir -p /config
    chown -R "$PUID:$PGID" /config || true

    # Other mount points are only chowned if currently owned by root (0)
    # to avoid extremely slow recursive chown calls on huge pre-existing libraries
    for dir in /generated /metadata /cache /blobs; do
        if [ -d "$dir" ]; then
            if [ "$(stat -c '%u' "$dir" 2>/dev/null)" = "0" ]; then
                echo "Updating ownership of $dir to $PUID:$PGID..."
                chown -R "$PUID:$PGID" "$dir" || true
            fi
        fi
    done
fi

# Drop privileges and run Stash
echo "Starting Stash as user $STASH_USER..."
if command -v gosu >/dev/null 2>&1; then
    exec gosu "$STASH_USER" "$@"
elif command -v su-exec >/dev/null 2>&1; then
    exec su-exec "$STASH_USER" "$@"
elif command -v runuser >/dev/null 2>&1; then
    exec runuser -u "$STASH_USER" -- "$@"
else
    exec "$@"
fi
