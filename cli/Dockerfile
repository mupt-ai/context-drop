FROM golang:1.26.2-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/context-drop-server ./cmd/context-drop-server

FROM alpine:3.22
RUN apk add --no-cache ca-certificates \
	&& adduser -D -H -u 65532 appuser
USER appuser
COPY --from=build /out/context-drop-server /context-drop-server
EXPOSE 8080
ENTRYPOINT ["/context-drop-server"]
