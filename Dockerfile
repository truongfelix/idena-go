ARG GO_IMAGE=golang:1.26.9-bookworm@sha256:d9c68c2c51161e12fd77e4c6320687c9cd86e1af1e3ad6e6cd63ff970641453c
ARG RUNTIME_IMAGE=debian:bookworm-slim@sha256:60eac759739651111db372c07be67863818726f754804b8707c90979bda511df

FROM ${GO_IMAGE} AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download && go mod verify

COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=1 go build -mod=readonly -trimpath -buildvcs=false \
    -ldflags="-s -w -X main.version=${VERSION}" -o /out/idena .

FROM ${RUNTIME_IMAGE}

RUN groupadd --system --gid 10001 idena && \
    useradd --system --uid 10001 --gid idena --create-home idena && \
    install -d -o idena -g idena -m 0700 /home/idena/datadir
COPY --from=builder --chown=idena:idena /out/idena /usr/local/bin/idena

ENV HOME=/home/idena
USER 10001:10001
WORKDIR /home/idena
VOLUME ["/home/idena/datadir"]

ENTRYPOINT ["/usr/local/bin/idena"]
