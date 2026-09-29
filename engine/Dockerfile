FROM golang:1.25-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /out/engine ./cmd

FROM alpine:3.20
WORKDIR /app
COPY --from=build /out/engine /usr/local/bin/engine
COPY agents ./agents
ENTRYPOINT ["engine"]
