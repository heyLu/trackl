FROM docker.io/golang:1.21-alpine3.19 as builder

# gcc and libc-dev for sqlite
RUN apk add --no-cache make gcc libc-dev

WORKDIR /build

COPY . .

RUN make htmx.min.js

RUN go build .

FROM alpine:3.19

RUN apk add --no-cache shadow && useradd --home-dir /dev/null --shell /bin/false trackl && apk del shadow
USER trackl

VOLUME /app/data

CMD /app/trackl -addr 0.0.0.0:5000 -db-path /app/data/trackl.db

COPY --from=builder /build/trackl /app/
