#!/bin/sh
set -eu
ssh-keygen -A >/dev/null
echo "root:${PASSWORD:-testpass}" | chpasswd
echo "forward: sshd ready (password auth, tcp forwarding disabled)"
exec /usr/sbin/sshd -D -e
