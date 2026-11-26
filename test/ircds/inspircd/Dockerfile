# syntax=docker/dockerfile:1
# InspIRCd 4 (https://www.inspircd.org) built from the release tag's source
# archive with ssl_openssl: a test fixture for the setup probe (test/ircds),
# not a stack image.
FROM debian:trixie-20261123-slim@sha256:8aa5a7bf5a1955bddbe7d74fcdd0b91ab30e74362a8b73af672b0481b35b44a0 AS build
ARG INSPIRCD_VERSION=4.12.1
ARG INSPIRCD_SHA256=d88d16014349c572776d5dc0a55e4b28e61caba857b5fbee47c3a49ca1591fef
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl build-essential pkg-config perl libssl-dev \
 && rm -rf /var/lib/apt/lists/* \
 && useradd -r -u 10001 -m -d /home/ircd ircd
USER ircd
WORKDIR /home/ircd/src
RUN curl -fsSLo inspircd.tar.gz "https://github.com/inspircd/inspircd/archive/refs/tags/v${INSPIRCD_VERSION}.tar.gz" \
 && echo "${INSPIRCD_SHA256}  inspircd.tar.gz" | sha256sum -c - \
 && tar xf inspircd.tar.gz --strip-components=1
RUN ./configure --enable-extras ssl_openssl --disable-auto-extras --disable-interactive \
 && ./configure --disable-auto-extras --disable-interactive --disable-ownership --prefix=/home/ircd/inspircd \
 && make -j"$(nproc)" install

FROM debian:trixie-20261123-slim@sha256:8aa5a7bf5a1955bddbe7d74fcdd0b91ab30e74362a8b73af672b0481b35b44a0
RUN apt-get update && apt-get install -y --no-install-recommends openssl libssl3t64 libstdc++6 netcat-openbsd procps \
 && rm -rf /var/lib/apt/lists/* \
 && useradd -r -u 10001 -m -d /home/ircd ircd
COPY --from=build --chown=ircd /home/ircd/inspircd /home/ircd/inspircd
COPY gencert.sh /usr/local/bin/gencert
COPY --chown=ircd inspircd/inspircd.conf /home/ircd/inspircd/conf/inspircd.conf
# extra.conf: test/ircds/run.sh apply appends to it before a rehash.
RUN echo '# test/ircds/run.sh apply' > /home/ircd/inspircd/conf/extra.conf \
 && echo '# test/ircds/run.sh apply-first' > /home/ircd/inspircd/conf/extra-first.conf && echo 'InspIRCd test fixture' > /home/ircd/inspircd/conf/motd.txt \
 && chown ircd:ircd /home/ircd/inspircd/conf/extra.conf /home/ircd/inspircd/conf/extra-first.conf /home/ircd/inspircd/conf/motd.txt \
 && mkdir -p /certs && chown ircd /certs
USER ircd
WORKDIR /home/ircd/inspircd
EXPOSE 6667 6697
CMD ["sh", "-c", "gencert /certs irc.insp.test && exec bin/inspircd --nofork --nopid"]
