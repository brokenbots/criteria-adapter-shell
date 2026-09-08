# syntax=docker/dockerfile:1

# Multi-stage build for the criteria-adapter-shell remote adapter image.
# The published image runs the adapter binary directly as a phone-home
# container (CRITERIA_REMOTE_HOST must be supplied at runtime).

FROM --platform=$BUILDPLATFORM golang:1.26.6-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY *.go ./

ARG TARGETOS
ARG TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-w -s" -o /out/criteria-adapter-shell .

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build --chown=nonroot:nonroot /out/criteria-adapter-shell /usr/local/bin/criteria-adapter-shell

USER nonroot:nonroot

ENTRYPOINT ["/usr/local/bin/criteria-adapter-shell"]
