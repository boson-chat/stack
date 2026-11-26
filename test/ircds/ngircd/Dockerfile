# syntax=docker/dockerfile:1
# ngIRCd (https://ngircd.barton.de) built from the release tarball with
# OpenSSL: a test fixture for the setup probe (test/ircds), not a stack image.
FROM debian:trixie-20261123-slim@sha256:8aa5a7bf5a1955bddbe7d74fcdd0b91ab30e74362a8b73af672b0481b35b44a0 AS build
ARG NGIRCD_VERSION=28
ARG NGIRCD_SHA256=b48ba320a931d445ae335c47f88a9406a20f5c71c623bee5f7755d0522d435ee
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates curl xz-utils build-essential libssl-dev zlib1g-dev pkg-config \
 && rm -rf /var/lib/apt/lists/*
WORKDIR /src
RUN curl -fsSLo ngircd.tar.xz "https://github.com/ngircd/ngircd/releases/download/rel-${NGIRCD_VERSION}/ngircd-${NGIRCD_VERSION}.tar.xz" \
 && echo "${NGIRCD_SHA256}  ngircd.tar.xz" | sha256sum -c - \
 && tar xf ngircd.tar.xz --strip-components=1
RUN ./configure --prefix=/usr/local --sysconfdir=/etc/ngircd --with-openssl --with-zlib \
 && make -j"$(nproc)" && make install DESTDIR=/out

FROM debian:trixie-20261123-slim@sha256:8aa5a7bf5a1955bddbe7d74fcdd0b91ab30e74362a8b73af672b0481b35b44a0
RUN apt-get update && apt-get install -y --no-install-recommends openssl zlib1g libssl3t64 netcat-openbsd procps \
 && rm -rf /var/lib/apt/lists/* \
 && useradd -r -u 10001 -m -d /home/ircd ircd
COPY --from=build /out/usr/local/ /usr/local/
COPY gencert.sh /usr/local/bin/gencert
COPY ngircd/ngircd.conf /etc/ngircd/ngircd.conf
# conf.d: the services link (mounted by the compose pairing) and extra.conf,
# which test/ircds/run.sh apply appends to before a rehash (SIGHUP).
RUN mkdir -p /etc/ngircd/conf.d /certs && touch /etc/ngircd/conf.d/extra.conf /etc/ngircd/ngircd.motd \
 && chown -R ircd /etc/ngircd/conf.d /certs
USER ircd
EXPOSE 6667 6697
CMD ["sh", "-c", "gencert /certs irc.ngircd.test && exec ngircd -n -f /etc/ngircd/ngircd.conf"]
