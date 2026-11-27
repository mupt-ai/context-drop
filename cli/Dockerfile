FROM golang:1.26.2-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/context-drop-server ./cmd/context-drop-server

# Mods run in Deno, which needs glibc.
FROM denoland/deno:bin-2.9.5 AS deno

FROM debian:bookworm-slim
RUN apt-get update -q && apt-get install -yq --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/* \
	&& useradd -M -u 65532 appuser
COPY --from=deno /deno /usr/local/bin/deno
COPY --from=build /out/context-drop-server /context-drop-server
USER appuser
ENV CONTEXT_DROP_DENO=/usr/local/bin/deno
EXPOSE 8080
ENTRYPOINT ["/context-drop-server"]
