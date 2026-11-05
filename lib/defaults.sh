# Copyright (c) 2026-present Douglas Hoard. All rights reserved.
# Use of this source code is governed by the MIT license,
# which can be found in the LICENSE file.

# The address the deployment uses when nothing else names one. This is the only
# copy in the shell scripts: install.sh reads it, through lib/deploy.sh, which
# sources this file.
#
# distribution/gassaway.service does NOT read this: its --host 127.0.0.1 is fixed on
# purpose, because the unit's sandbox (IPAddressDeny=any, IPAddressAllow=localhost)
# is only correct for a loopback bind. Changing the default here must not change it.
DEFAULT_HOST="127.0.0.1"
DEFAULT_PORT="4029"
