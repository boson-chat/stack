#!/bin/sh
# crond doesn't pass the container environment to jobs, so save it once and
# load it in each job. Schedules are overridable via env.
set -eu
umask 077
export -p | grep -v -E ' (HOME|PWD|SHLVL|HOSTNAME|OLDPWD)=' > /etc/api.env
mkdir -p /etc/crontabs
cat > /etc/crontabs/root <<CRON
# verify: re-check every directory listing's DNS TXT record (daily)
${API_CRON_VERIFY:-0 3 * * *} . /etc/api.env; cd /app && ./main cron --mode=verify
# health: probe every verified server (every 15 minutes)
${API_CRON_HEALTH:-*/15 * * * *} . /etc/api.env; cd /app && ./main cron --mode=health
CRON
echo "api-cron: schedules"; grep -v '^#' /etc/crontabs/root | cut -d. -f1
exec crond -f -l 6 -L /dev/stdout
