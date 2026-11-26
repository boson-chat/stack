# syntax=docker/dockerfile:1
# Anope 2.1 (https://www.anope.org) built from the release tag's source
# archive, with httpd + jsonrpc + rpc_user/rpc_data/rpc_system loaded: a test
# fixture for the setup probe (test/ircds), not a stack image. One image for
# both pairings: --config picks ngircd.conf or unrealircd.conf.
FROM debian:trixie-20261123-slim@sha256:8aa5a7bf5a1955bddbe7d74fcdd0b91ab30e74362a8b73af672b0481b35b44a0 AS build
ARG ANOPE_VERSION=2.1.28
ARG ANOPE_SHA256=cb80e8187646e149efa996d6207c775d92fd5c14712e5cdc00731c12e167a005
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl build-essential cmake \
 && rm -rf /var/lib/apt/lists/* \
 && useradd -r -u 10001 -m -d /home/anope anope
USER anope
WORKDIR /home/anope/src
RUN curl -fsSLo anope.tar.gz "https://github.com/anope/anope/archive/refs/tags/${ANOPE_VERSION}.tar.gz" \
 && echo "${ANOPE_SHA256}  anope.tar.gz" | sha256sum -c - \
 && tar xf anope.tar.gz --strip-components=1
RUN cmake -B build -S . -D INSTDIR=/home/anope/services -D CMAKE_BUILD_TYPE=Release \
 && make -C build -j"$(nproc)" install

FROM debian:trixie-20261123-slim@sha256:8aa5a7bf5a1955bddbe7d74fcdd0b91ab30e74362a8b73af672b0481b35b44a0
RUN apt-get update && apt-get install -y --no-install-recommends libstdc++6 procps curl \
 && rm -rf /var/lib/apt/lists/* \
 && useradd -r -u 10001 -m -d /home/anope anope
COPY --from=build --chown=anope /home/anope/services /home/anope/services
WORKDIR /home/anope/services
# Test-friendly NickServ: no email, no confirmation, no wait between
# registrations (the smoke test and the probe register throwaway accounts).
RUN sed -i -e 's/^\(\s*\)forceemail = yes/\1forceemail = no/' \
      -e 's/^\(\s*\)registration = "code"/\1registration = "none"/' \
      -e 's/^\(\s*\)regdelay = 15m/\1regdelay = 1s/' conf/nickserv.example.conf \
 && grep -q 'forceemail = no' conf/nickserv.example.conf && grep -q 'registration = "none"' conf/nickserv.example.conf
COPY --chown=anope anope/services.conf anope/ngircd.conf anope/unrealircd.conf conf/
USER anope
EXPOSE 8080
CMD ["bin/anope", "--nofork", "--nopid", "--config=ngircd.conf"]
