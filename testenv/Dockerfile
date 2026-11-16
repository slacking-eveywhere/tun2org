# tun2org e2e test images. All stages are alpine for speed; the client is a
# multi-stage build so the ntun binaries are compiled from the repo source.

ARG ALPINE=alpine:3.21

# keygen: one-shot ssh key generator for the bastion hop
FROM ${ALPINE} AS keygen
RUN apk add --no-cache openssh-client
ENTRYPOINT ["sh", "-c", "ssh-keygen -q -t ed25519 -N '' -C ntun-e2e -f /keys/id_ed25519"]

# bastion: sshd with pubkey auth; allows tcp forwarding (ssh -W)
FROM ${ALPINE} AS bastion
RUN apk add --no-cache openssh
RUN mkdir -p /root/.ssh
COPY testenv/sshd-bastion.conf /etc/ssh/sshd_config
COPY testenv/entry-bastion.sh /entry.sh
EXPOSE 22
ENTRYPOINT ["/entry.sh"]

# forward: sshd with password auth; NO tcp forwarding (the real constraint)
FROM ${ALPINE} AS forward
RUN apk add --no-cache openssh
COPY testenv/sshd-forward.conf /etc/ssh/sshd_config
COPY testenv/entry-forward.sh /entry.sh
EXPOSE 22
ENTRYPOINT ["/entry.sh"]

# target: http + tcp echo + udp echo endpoints
FROM ${ALPINE} AS target
RUN apk add --no-cache python3
COPY testenv/endpoints.py /endpoints.py
EXPOSE 8080 9000 9001
ENTRYPOINT ["python3", "/endpoints.py"]

# client: real ntun + ntun-agent built from the repo
FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG COMMIT=e2e-local
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X ntun-ng/internal/version.Commit=${COMMIT}" \
    -o /out/ntun ./cmd/ntun \
    && CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w -X ntun-ng/internal/version.Commit=${COMMIT}" \
    -o /out/ntun-agent ./cmd/ntun-agent

FROM ${ALPINE} AS client
RUN apk add --no-cache curl openssh-client python3
COPY --from=build /out/ntun /usr/local/bin/ntun
COPY --from=build /out/ntun-agent /usr/local/bin/ntun-agent
COPY testenv/client.sh /test/client.sh
COPY testenv/probe.py /test/probe.py
ENTRYPOINT ["/bin/sh", "/test/client.sh"]
