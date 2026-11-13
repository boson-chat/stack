# syntax=docker/dockerfile:1
FROM debian:bookworm-slim AS build

ARG ANOPE_VERSION=2.0.20
ARG ANOPE_SHA256=646cc6d24dea62fac54285780ebb05ad794a3dbabe6aca1ae6c69a939b0a0734

ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
      build-essential \
      cmake \
      pkg-config \
      libssl-dev \
      libsqlite3-dev \
      ca-certificates \
      curl \
 && rm -rf /var/lib/apt/lists/*

RUN useradd -m -u 10001 -s /bin/sh anope
USER anope
WORKDIR /home/anope

RUN curl -fsSLo "anope-${ANOPE_VERSION}.tar.gz" \
      "https://github.com/anope/anope/archive/refs/tags/${ANOPE_VERSION}.tar.gz" \
 && echo "${ANOPE_SHA256}  anope-${ANOPE_VERSION}.tar.gz" | sha256sum -c - \
 && tar xzf "anope-${ANOPE_VERSION}.tar.gz" \
 && rm "anope-${ANOPE_VERSION}.tar.gz"

WORKDIR /home/anope/anope-${ANOPE_VERSION}

# Enable optional modules by symlinking from modules/extra into modules/
RUN ln -s extra/m_sqlite.cpp modules/m_sqlite.cpp \
 && ln -s extra/m_ssl_openssl.cpp modules/m_ssl_openssl.cpp

RUN mkdir build && cd build \
 && cmake -DINSTDIR:STRING=/home/anope/anope -DCMAKE_BUILD_TYPE=Release .. \
 && make -j"$(nproc)" \
 && make install

# --- runtime stage ---
FROM debian:bookworm-slim

ENV DEBIAN_FRONTEND=noninteractive
RUN apt-get update && apt-get install -y --no-install-recommends \
      libssl3 \
      libsqlite3-0 \
      ca-certificates \
      msmtp-mta \
      tini \
 && rm -rf /var/lib/apt/lists/*

RUN useradd -m -u 10001 -s /bin/sh anope

COPY --from=build --chown=anope:anope /home/anope/anope /home/anope/anope

COPY entrypoint.sh /usr/local/bin/entrypoint.sh

USER anope
WORKDIR /home/anope/anope

ENTRYPOINT ["/usr/bin/tini", "--", "/usr/local/bin/entrypoint.sh"]
CMD ["/home/anope/anope/bin/services", "-nofork"]
