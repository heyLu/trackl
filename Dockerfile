FROM docker.io/golang:1.21-alpine3.19 as builder

# gcc and libc-dev for sqlite, git for vcs listing in /stats page
RUN apk add --no-cache gcc libc-dev git

WORKDIR /build

COPY . .
RUN go build .

FROM alpine:3.19

RUN apk add --no-cache shadow && useradd --home-dir /dev/null --shell /bin/false trackl && apk del shadow
USER trackl

VOLUME /app/data

CMD /app/trackl -addr 0.0.0.0:5555

COPY --from=builder /build/trackl /app/
