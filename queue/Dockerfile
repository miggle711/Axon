FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/queue ./cmd

FROM alpine:3.20
COPY --from=build /out/queue /usr/local/bin/queue
ENTRYPOINT ["queue"]
