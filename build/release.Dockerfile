# syntax=docker/dockerfile:1

# ponytail: official Go 1.26.5 lacks Bullseye; combine pinned toolchain and pinned Bullseye image until upstream publishes one.
FROM golang:1.26.5@sha256:55d3b3d8ea3ae125d21f528f392a7ae0efbd9c69cbac7a479921121af3c7b2a2 AS go126
FROM golang:1.20-bullseye@sha256:8fd44351d719dbf3f86ad095f9056040c33ccdeac9a18b54dec81fd152a31853 AS builder

RUN rm -rf /usr/local/go
COPY --from=go126 /usr/local/go /usr/local/go
ENV PATH="/usr/local/go/bin:${PATH}"
WORKDIR /src

RUN go version | grep -F 'go1.26.5 linux/amd64' \
    && ldd --version | head -n 1 | grep -F '2.31'

COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN make release DIST_DIR=/out && make verify-release DIST_DIR=/out

FROM scratch AS artifacts
COPY --from=builder /out/ /
