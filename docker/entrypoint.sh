#!/bin/sh
set -e

# Default PUID/PGID if not specified
PUID=${PUID:-0}
PGID=${PGID:-0}

# Handle rootless/USER mapping if running as root
if [ "$(id -u)" = "0" ] && [ "$PUID" -ne 0 ] && [ "$PGID" -ne 0 ]; then
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
        local gid="$1"
        local group_name="$2"
        if [ -n "$gid" ] && [ "$gid" -gt 0 ]; then
            local existing_group=$(getent group "$gid" | cut -d: -f1)
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

    # Migrate legacy data from /root/.stash if applicable
    if [ -d /root/.stash ] && [ ! -f /config/config.yml ]; then
        echo "Migrating legacy config from /root/.stash to /config..."
        mkdir -p /config
        cp -rp /root/.stash/* /config/ 2>/dev/null || true
    fi

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

    # Install Python dependencies if requirements.txt exists
    if [ -f /config/requirements.txt ]; then
        echo "Installing Python dependencies from /config/requirements.txt..."
        pip3 install --no-cache-dir -r /config/requirements.txt 2>/dev/null || pip install --no-cache-dir -r /config/requirements.txt 2>/dev/null || true
    fi

    # Drop privileges and run Stash
    echo "Starting Stash as user $STASH_USER..."
    if command -v su-exec >/dev/null 2>&1; then
        exec su-exec "$STASH_USER" "$@"
    elif command -v gosu >/dev/null 2>&1; then
        exec gosu "$STASH_USER" "$@"
    elif command -v runuser >/dev/null 2>&1; then
        exec runuser -u "$STASH_USER" -- "$@"
    else
        exec "$@"
    fi
else
    # Not running as root or PUID/PGID not set/root
    # Just run the application directly
    exec "$@"
fi
