FROM golang:alpine AS builder
WORKDIR /src
ENV CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOTOOLCHAIN=local
COPY go.mod ./
COPY vendor ./vendor
COPY main.go ./
COPY frontend ./frontend
RUN go version && go build -mod=vendor -trimpath -ldflags="-s -w" -o /out/sre-agent .

FROM alpine:latest
RUN addgroup -S sre && adduser -S -G sre sre
COPY --from=builder /out/sre-agent /usr/local/bin/sre-agent
USER sre
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/sre-agent"]
