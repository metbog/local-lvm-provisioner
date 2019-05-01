FROM alpine:latest

COPY bin/run.sh /run.sh
COPY bin/local-lvm-provisioner /bin/local-lvm-provisioner
COPY bin/local-lvm-provisioner.sh /usr/share/local-lvm-provisioner.sh
COPY bin/local-lvm-provisioner.service /usr/share/local-lvm-provisioner.service

RUN apk add lvm2 e2fsprogs && chmod +x /run.sh

ENTRYPOINT ["/run.sh"]
