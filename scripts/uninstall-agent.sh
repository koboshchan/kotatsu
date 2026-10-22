#!/bin/sh
set -eu
[ "$(id -u)" = 0 ] || { echo "Run as root" >&2; exit 1; }
systemctl disable --now kotatsu-agent.service
rm -f /etc/systemd/system/kotatsu-agent.service /usr/local/bin/kotatsu-agent
systemctl daemon-reload
if [ "${1:-}" = --purge ]; then
  rm -rf /var/lib/kotatsu
  if getent passwd kotatsu >/dev/null; then userdel kotatsu; fi
else
  echo "Identity retained at /var/lib/kotatsu; pass --purge to remove the credentials and service user."
fi
echo "Service removed. Revoke the machine credential on your server if this host is retired."
