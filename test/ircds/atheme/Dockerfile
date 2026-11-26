# syntax=docker/dockerfile:1
# Atheme (https://atheme.github.io) built from source with misc/httpd,
# transport/jsonrpc and transport/xmlrpc: a test fixture for the setup probe
# (test/ircds), not a stack image.
#
# Pinned to a master commit (7.3.0-rc2), not the newest release: 7.2.12
# speaks InspIRCd link protocol 1202, and InspIRCd 4 only links with 1205+.
# The commit pins the libmowgli-2 and modules/contrib submodules too.
FROM debian:trixie-20261123-slim@sha256:8aa5a7bf5a1955bddbe7d74fcdd0b91ab30e74362a8b73af672b0481b35b44a0 AS build
ARG ATHEME_COMMIT=bf55352ddf7ecdacc9c3f7058c5ba05f28887676
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates git build-essential pkg-config libssl-dev \
 && rm -rf /var/lib/apt/lists/* \
 && useradd -r -u 10001 -m -d /home/atheme atheme
USER atheme
WORKDIR /home/atheme/src
RUN git init -q . && git remote add origin https://github.com/atheme/atheme.git \
 && git fetch -q --depth 1 origin "$ATHEME_COMMIT" && git checkout -q FETCH_HEAD \
 && test "$(git rev-parse HEAD)" = "$ATHEME_COMMIT" \
 && git submodule update -q --init --depth 1 libmowgli-2 modules/contrib
RUN ./configure --prefix=/home/atheme/atheme --disable-nls --without-ldap --without-pcre --without-cracklib --without-idn \
 && make -j"$(nproc)" && make install

FROM debian:trixie-20261123-slim@sha256:8aa5a7bf5a1955bddbe7d74fcdd0b91ab30e74362a8b73af672b0481b35b44a0
RUN apt-get update && apt-get install -y --no-install-recommends libssl3t64 procps curl \
 && rm -rf /var/lib/apt/lists/* \
 && useradd -r -u 10001 -m -d /home/atheme atheme
COPY --from=build --chown=atheme /home/atheme/atheme /home/atheme/atheme
COPY --chown=atheme atheme/atheme.conf /home/atheme/atheme/etc/atheme.conf
USER atheme
WORKDIR /home/atheme/atheme
EXPOSE 8080
CMD ["bin/atheme-services", "-n"]
