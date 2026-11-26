# syntax=docker/dockerfile:1
# UnrealIRCd 6 (https://www.unrealircd.org) built from the release tarball: a
# test fixture for the setup probe (test/ircds), not a stack image.
FROM debian:trixie-20261123-slim@sha256:8aa5a7bf5a1955bddbe7d74fcdd0b91ab30e74362a8b73af672b0481b35b44a0 AS build
ARG UNREALIRCD_VERSION=6.2.7.1
ARG UNREALIRCD_SHA256=4e372ba071f3211d34c5a3515af77fc2b275eb692e43e55c423e201b60958c3f
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl build-essential pkg-config \
      libssl-dev libpcre2-dev libargon2-dev libsodium-dev libc-ares-dev libjansson-dev \
 && rm -rf /var/lib/apt/lists/* \
 && useradd -r -u 10001 -m -d /home/ircd ircd
USER ircd
WORKDIR /home/ircd/src
RUN curl -fsSLo unrealircd.tar.gz "https://www.unrealircd.org/downloads/unrealircd-${UNREALIRCD_VERSION}.tar.gz" \
 && echo "${UNREALIRCD_SHA256}  unrealircd.tar.gz" | sha256sum -c - \
 && tar xf unrealircd.tar.gz --strip-components=1
# Boson's optional +draft/react module (the setup wizard's snippet; a copy
# of images/setup/internal/stack/snippets/boson-react.c): built, not loaded
# until a snippet says loadmodule "third/boson-react".
COPY --chown=ircd unrealircd/boson-react.c src/modules/third/boson-react.c
# ./configure directly (what ./Config -quick runs), installed under /ircd.
RUN B=/home/ircd/unrealircd; ./configure --enable-ssl --enable-dynamic-linking --with-system-argon2 \
      --with-bindir=$B/bin --with-datadir=$B/data --with-pidfile=$B/data/unrealircd.pid \
      --with-controlfile=$B/data/unrealircd.ctl --with-confdir=$B/conf --with-modulesdir=$B/modules \
      --with-logdir=$B/logs --with-cachedir=$B/cache --with-docdir=$B/doc --with-tmpdir=$B/tmp \
      --with-privatelibdir=$B/lib --with-scriptdir=$B --with-nick-history=2000 --with-permissions=0600 \
 && make -j"$(nproc)" && make install

FROM debian:trixie-20261123-slim@sha256:8aa5a7bf5a1955bddbe7d74fcdd0b91ab30e74362a8b73af672b0481b35b44a0
RUN apt-get update && apt-get install -y --no-install-recommends openssl libssl3t64 libpcre2-8-0 libargon2-1 libsodium23 libc-ares2 libjansson4 netcat-openbsd procps \
 && rm -rf /var/lib/apt/lists/* \
 && useradd -r -u 10001 -m -d /home/ircd ircd
COPY --from=build --chown=ircd /home/ircd/unrealircd /home/ircd/unrealircd
COPY gencert.sh /usr/local/bin/gencert
COPY --chown=ircd unrealircd/unrealircd.conf /home/ircd/unrealircd/conf/unrealircd.conf
# extra.conf: test/ircds/run.sh apply appends to it before a rehash.
RUN touch /home/ircd/unrealircd/conf/extra.conf && chown ircd /home/ircd/unrealircd/conf/extra.conf \
 && mkdir -p /certs && chown ircd /certs
USER ircd
WORKDIR /home/ircd/unrealircd
EXPOSE 6667 6697
CMD ["sh", "-c", "gencert /certs irc.unreal.test && exec bin/unrealircd -F"]
