FROM alpine:3.9.3

COPY files/run.sh /run.sh

RUN apk add lvm2 e2fsprogs && chmod +x /run.sh
ENTRYPOINT ["/run.sh"]
