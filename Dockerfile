FROM golang:1.25.2-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ENV CGO_ENABLED=0

FROM build AS test
RUN go test ./...

FROM build AS binary
RUN go build -trimpath -ldflags="-s -w" -o /newtond .

FROM debian:bookworm-slim
RUN apt-get update && apt-get install -y --no-install-recommends ca-certificates && rm -rf /var/lib/apt/lists/*
COPY --from=binary /newtond /usr/local/bin/newtond
USER 10001:10001
ENV NEWTOND_DB=/data/newtond.db
EXPOSE 8080/tcp 5514/udp
ENTRYPOINT ["/usr/local/bin/newtond"]
