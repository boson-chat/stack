FROM alpine:3.22
RUN apk add --no-cache bash coreutils findutils
COPY certsync.sh /usr/local/bin/certsync.sh
ENTRYPOINT ["/usr/local/bin/certsync.sh"]
