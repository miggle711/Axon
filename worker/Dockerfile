FROM golang:1.25-alpine AS build
WORKDIR /src
# no go.sum here: axon-worker has no external dependencies (stdlib only)
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 go build -o /out/worker ./cmd

FROM alpine:3.20
COPY --from=build /out/worker /usr/local/bin/worker
ENTRYPOINT ["worker"]
