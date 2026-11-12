# Single-container backend: nats-server (JetStream) + migrations + api + worker,
# supervised by cmd/supervise. Built for the Render free web service
# (512 MB RAM, no persistent disk); see docs/deploy.md.
#
#   docker build -t horizon-go .
#   docker run --rm -p 8080:8080 -e DATABASE_URL=... -e LLM_PROVIDER=groq -e GROQ_API_KEY=... \
#     -e EMBED_PROVIDER=gemini -e GEMINI_API_KEY=... horizon-go

FROM golang:1.26-alpine AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOFLAGS=-trimpath
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ ./
RUN go build -ldflags="-s -w" -o /out/api ./cmd/api \
 && go build -ldflags="-s -w" -o /out/worker ./cmd/worker \
 && go build -ldflags="-s -w" -o /out/supervise ./cmd/supervise

# Official static nats-server binary.
FROM nats:2.15-scratch AS nats

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=nats /nats-server /usr/local/bin/nats-server
COPY --from=build /out/ /app/
COPY deploy/nats.conf /etc/nats/nats.conf
ENV NATS_SERVER_BIN=/usr/local/bin/nats-server \
    NATS_CONFIG=/etc/nats/nats.conf \
    PORT=8080 \
    SCORE_WORKERS=1 \
    GOMAXPROCS=1 \
    NATS_GOMEMLIMIT=64MiB \
    API_GOMEMLIMIT=96MiB \
    WORKER_GOMEMLIMIT=160MiB
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/app/supervise"]
