FROM golang:1.24-alpine AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /stashy ./cmd/stashy

FROM alpine:3.21
RUN apk add --no-cache ca-certificates
COPY --from=build /stashy /usr/local/bin/stashy
COPY public/ /app/public/

WORKDIR /app
EXPOSE 8080

ENTRYPOINT ["stashy"]
