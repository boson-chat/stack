# syntax=docker/dockerfile:1
FROM debian:bookworm-slim AS build

ARG UNREAL_VERSION=6.2.5
ARG UNREAL_SHA256=136854437d9b34b7f83a2d8227a8aae7cda505dd3db8c5e70f711eecf494be2c

ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
      build-essential \
      pkg-config \
      libssl-dev \
      libargon2-dev \
      libsodium-dev \
      libpcre2-dev \
      libc-ares-dev \
      libjansson-dev \
      libcurl4-openssl-dev \
      ca-certificates \
      curl \
 && rm -rf /var/lib/apt/lists/*

RUN useradd -m -u 10000 -s /bin/sh unrealircd
USER unrealircd
WORKDIR /home/unrealircd

RUN curl -fsSLO "https://www.unrealircd.org/downloads/unrealircd-${UNREAL_VERSION}.tar.gz" \
 && echo "${UNREAL_SHA256}  unrealircd-${UNREAL_VERSION}.tar.gz" | sha256sum -c - \
 && tar xzf "unrealircd-${UNREAL_VERSION}.tar.gz" \
 && rm "unrealircd-${UNREAL_VERSION}.tar.gz"

WORKDIR /home/unrealircd/unrealircd-${UNREAL_VERSION}

# Pre-seed Config answers so the build is non-interactive
RUN printf '%s\n' \
  'BASEPATH="/home/unrealircd/unrealircd"' \
  'BINDIR="/home/unrealircd/unrealircd/bin"' \
  'DATADIR="/home/unrealircd/unrealircd/data"' \
  'CONFDIR="/home/unrealircd/unrealircd/conf"' \
  'MODULESDIR="/home/unrealircd/unrealircd/modules"' \
  'LOGDIR="/home/unrealircd/unrealircd/logs"' \
  'CACHEDIR="/home/unrealircd/unrealircd/cache"' \
  'DOCDIR="/home/unrealircd/unrealircd/doc"' \
  'TMPDIR="/home/unrealircd/unrealircd/tmp"' \
  'PRIVATELIBDIR="/home/unrealircd/unrealircd/lib"' \
  'MAXCONNECTIONS_REQUEST="auto"' \
  'NICKNAMEHISTORYLENGTH="2000"' \
  'GEOIP="none"' \
  'DEFPERM="0600"' \
  'SSLDIR=""' \
  'REMOTEINC="1"' \
  'CURLDIR=""' \
  'NOOPEROVERRIDE=""' \
  'OPEROVERRIDEVERIFY=""' \
  'GENCERTIFICATE="1"' \
  'SANITIZER=""' \
  'EXTRAPARA=""' \
  'ADVANCED=""' \
  > config.settings

RUN ./Config -quick && make -j"$(nproc)" && make install

# --- runtime stage ---
FROM debian:bookworm-slim

ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
      libssl3 \
      libargon2-1 \
      libsodium23 \
      libpcre2-8-0 \
      libc-ares2 \
      libjansson4 \
      libcurl4 \
      ca-certificates \
      gettext-base \
      tini \
 && rm -rf /var/lib/apt/lists/*

RUN useradd -m -u 10000 -s /bin/sh unrealircd

COPY --from=build --chown=unrealircd:unrealircd /home/unrealircd/unrealircd /home/unrealircd/unrealircd

COPY --chown=unrealircd:unrealircd unrealircd.conf.tmpl /home/unrealircd/templates/unrealircd.conf.tmpl
COPY entrypoint.sh /usr/local/bin/entrypoint.sh

USER unrealircd
WORKDIR /home/unrealircd/unrealircd

# 6667 plaintext (optional), 6697 TLS, 7000 server links (services; internal only)
EXPOSE 6667 6697 7000

ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/entrypoint.sh"]
CMD ["/home/unrealircd/unrealircd/bin/unrealircd", "-F"]
