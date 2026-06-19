# Builder runs natively on the host arch (BUILDPLATFORM) and cross-compiles
# the Go binary to TARGETARCH — much faster than emulating the builder.
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS builder

ARG TARGETOS
ARG TARGETARCH

RUN apk add --no-cache git

WORKDIR /src

COPY backend/go.mod ./
COPY backend/ .
RUN go mod tidy
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -o /out/sparklabx-backend ./cmd/server

FROM alpine:3.24

# notebook-lite: no terraform / crane needed (no cloud provisioning, no
# container image push to per-session ECRs). Just a tiny runtime image.
RUN apk add --no-cache ca-certificates tzdata

WORKDIR /app

COPY --from=builder /out/sparklabx-backend /app/sparklabx-backend

EXPOSE 10000

CMD ["/app/sparklabx-backend"]
