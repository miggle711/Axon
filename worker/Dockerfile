FROM golang:1.25-alpine AS build
WORKDIR /src
# no go.sum here: axon-worker has no external dependencies (stdlib only)
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 go build -o /out/worker ./cmd

FROM alpine:3.20
# pandoc is needed for the pandoc_to_markdown subprocess tool config
# (worker/subprocess_tools/pandoc_to_markdown.json, see #42) - without
# it the worker still starts fine, it just warns and skips registering
# that one tool.
RUN apk add --no-cache pandoc
WORKDIR /app
COPY --from=build /out/worker /usr/local/bin/worker
COPY subprocess_tools ./subprocess_tools
ENTRYPOINT ["worker"]
