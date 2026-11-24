#!/bin/sh
# Installs the pocketsudo helper on this machine. Run as root.
#   sudo scripts/install-host.sh path/to/pocketsudo [existing-config.json]
# Afterwards: sudo pocketsudo pair, sudo pocketsudo test, sudo pocketsudo install-pam
set -eu
bin=${1:?usage: install-host.sh BINARY [CONFIG]}
[ "$(id -u)" = 0 ] || { echo "run as root" >&2; exit 1; }
install -o root -g root -m 0755 "$bin" /usr/local/bin/pocketsudo
install -d -o root -g root -m 0700 /etc/pocketsudo
if [ -n "${2:-}" ]; then
    install -o root -g root -m 0600 "$2" /etc/pocketsudo/config.json
fi
/usr/local/bin/pocketsudo version
