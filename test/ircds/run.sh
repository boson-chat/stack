#!/bin/sh
# IRC server fixtures (test/ircds/compose.yaml): bring a pairing up, apply a
# config snippet the way an admin would paste the setup wizard's output,
# smoke-test it. Each pairing is its own compose project
# (boson-ircds-<pairing>), so several can run at once.
#
#   test/ircds/run.sh up PAIRING        build + start, wait until linked and healthy
#   test/ircds/run.sh env PAIRING       print the addresses (KEY=value; source it)
#   test/ircds/run.sh apply PAIRING [FILE]
#                                       append FILE (or stdin) to the IRC server's
#                                       extra config and rehash; fails if the
#                                       server rejects it (the snippet is rolled back)
#   test/ircds/run.sh apply-first PAIRING [FILE]
#                                       the same, for a snippet that goes before the
#                                       server's own blocks (InspIRCd <connect>: the
#                                       first match wins); elsewhere the same as apply
#   test/ircds/run.sh reset PAIRING     empty the extra config and rehash
#   test/ircds/run.sh smoke PAIRING     connect, register a nick with NickServ, identify
#   test/ircds/run.sh logs PAIRING      the last LOG_TAIL (200) log lines of each container
#   test/ircds/run.sh down PAIRING      stop and remove (containers, volumes)
#
# PAIRING: ngircd-anope | unreal-anope | insp-atheme | ngircd
# IRCDS_BIND: the address the ports are published on (default 127.0.0.1;
# test/setup/run-addon.sh uses the machine's own, so containers reach them).
set -eu
here=$(cd "$(dirname "$0")" && pwd)
cmd=${1:-}
pairing=${2:-}

case $pairing in
  ngircd-anope) ircd=ngircd;      services=anope-ngircd; svcpkg=anope;  prefix=NGIRCD_ANOPE; tls=16697; plain=16667; rpc=18080 ;;
  unreal-anope) ircd=unrealircd;  services=anope-unreal; svcpkg=anope;  prefix=UNREAL_ANOPE; tls=26697; plain=26667; rpc=28080 ;;
  insp-atheme)  ircd=inspircd;    services=atheme;       svcpkg=atheme; prefix=INSP_ATHEME;  tls=36697; plain=36667; rpc=38080 ;;
  ngircd)       ircd=ngircd-solo; services=;             svcpkg=none;   prefix=NGIRCD;       tls=46697; plain=46667; rpc= ;;
  *) sed -n '2,27p' "$0" | sed 's/^# \{0,1\}//' >&2; exit 2 ;;
esac
# Port overrides, as in compose.yaml.
eval "tls=\${${prefix}_TLS_PORT:-$tls}; plain=\${${prefix}_PORT:-$plain}"
[ -n "$rpc" ] && eval "rpc=\${${prefix}_RPC_PORT:-$rpc}"

compose() { docker compose -f "$here/compose.yaml" -p "boson-ircds-$pairing" --profile "$pairing" "$@"; }

# Where each server keeps the file run.sh apply appends to.
case $ircd in
  ngircd|ngircd-solo) extra=/etc/ngircd/conf.d/extra.conf ;;
  unrealircd)         extra=/home/ircd/unrealircd/conf/extra.conf ;;
  inspircd)           extra=/home/ircd/inspircd/conf/extra.conf ;;
esac
first=$extra
[ "$ircd" = inspircd ] && first=/home/ircd/inspircd/conf/extra-first.conf

# rehash: reload the IRC server's config; fail (non-zero) if it was rejected.
rehash() {
  since=$(date -u +%Y-%m-%dT%H:%M:%SZ)
  case $ircd in
    unrealircd)
      # The control socket reports the rehash's outcome and errors.
      out=$(compose exec -T "$ircd" ./unrealircd rehash 2>&1) || true
      echo "$out" | grep -v '^\[advice\]'
      echo "$out" | grep -q 'Rehashed successfully' || return 1
      ;;
    inspircd)
      # InspIRCd reports the rehash (errors included) to the oper who asked.
      python3 "$here/rehash.py" --host "${IRCDS_BIND:-127.0.0.1}" --port "$plain" || return 1
      ;;
    ngircd|ngircd-solo)
      # ngIRCd keeps the old config if the new one doesn't parse; a config
      # test first tells us.
      out=$(compose exec -T "$ircd" ngircd --configtest -f /etc/ngircd/ngircd.conf 2>&1 </dev/null) || { echo "$out" | tail -n 20 >&2; return 1; }
      if echo "$out" | grep -qiE '(error|unknown variable|unknown section|invalid)'; then
        echo "$out" | grep -iE 'error|unknown|invalid' >&2
        return 1
      fi
      compose kill -s HUP "$ircd" >/dev/null
      sleep 1
      ;;
  esac
  # Still serving after the rehash?
  for i in $(seq 1 30); do
    compose exec -T "$ircd" grep -Eq ':1A29 [0-9A-F]+:0000 0A' /proc/net/tcp /proc/net/tcp6 2>/dev/null && return 0
    sleep 1
  done
  echo "$ircd stopped listening after the rehash" >&2
  compose logs --since "$since" "$ircd" >&2
  return 1
}

case $cmd in
  up)
    compose up -d --build --wait --wait-timeout 300
    ;;
  env)
    echo "IRC_HOST=${IRCDS_BIND:-127.0.0.1}"
    echo "IRC_TLS_PORT=$tls"
    echo "IRC_PORT=$plain"
    echo "IRC_SOFTWARE=$ircd"
    echo "SERVICES=$svcpkg"
    case $svcpkg in
      anope)  echo "SERVICES_RPC_URL=http://${IRCDS_BIND:-127.0.0.1}:$rpc/jsonrpc"; echo "SERVICES_RPC_TOKEN=fixture-rpc-token" ;;
      atheme) echo "SERVICES_RPC_URL=http://${IRCDS_BIND:-127.0.0.1}:$rpc/jsonrpc"; echo "SERVICES_XMLRPC_URL=http://${IRCDS_BIND:-127.0.0.1}:$rpc/xmlrpc" ;;
    esac
    ;;
  apply|apply-first)
    [ "$cmd" = apply-first ] && extra=$first
    file=${3:--}
    snippet=$(cat "$file")
    backup=$(compose exec -T "$ircd" cat "$extra")
    printf '\n%s\n' "$snippet" | compose exec -T "$ircd" sh -c "cat >> $extra"
    if ! rehash; then
      echo "rehash failed: rolling the snippet back" >&2
      printf '%s\n' "$backup" | compose exec -T "$ircd" sh -c "cat > $extra"
      rehash >/dev/null 2>&1 || true
      exit 1
    fi
    echo "applied to $ircd ($extra) and rehashed"
    ;;
  reset)
    echo '# test/ircds/run.sh apply' | compose exec -T "$ircd" sh -c "cat > $extra"
    [ "$first" = "$extra" ] || echo '# test/ircds/run.sh apply-first' | compose exec -T "$ircd" sh -c "cat > $first"
    rehash
    ;;
  smoke)
    python3 "$here/smoke.py" --host "${IRCDS_BIND:-127.0.0.1}" --port "$tls" --services "$svcpkg" ${rpc:+--rpc-url "http://${IRCDS_BIND:-127.0.0.1}:$rpc/jsonrpc"} ${SMOKE_ARGS:-}
    ;;
  logs)
    compose logs --no-color --tail "${LOG_TAIL:-200}"
    ;;
  down)
    compose down -v --remove-orphans
    ;;
  *)
    sed -n '2,27p' "$0" | sed 's/^# \{0,1\}//' >&2
    exit 2
    ;;
esac
