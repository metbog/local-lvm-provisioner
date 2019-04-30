FROM alpine:latest

COPY bin/local-lvm-provisioner /bin/local-lvm-provisioner
COPY bin/run.sh /run.sh

RUN apk add lvm2 e2fsprogs && chmod +x /run.sh

ENTRYPOINT ["/run.sh"]
