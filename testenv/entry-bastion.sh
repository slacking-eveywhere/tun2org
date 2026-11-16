#!/bin/sh
set -eu
ssh-keygen -A >/dev/null
cp /run/keys/id_ed25519.pub /root/.ssh/authorized_keys
chmod 700 /root/.ssh
chmod 600 /root/.ssh/authorized_keys
echo "bastion: sshd ready (pubkey auth)"
exec /usr/sbin/sshd -D -e
