FROM golang:alpine

EXPOSE 7070/tcp

RUN apk --update add git \
&& git clone https://github.com/MortenHarding/gopher-http-proxy.git \
&& cd gopher-http-proxy \
&& go build -o /go/httpproxy . \
&& cd /go \
&& rm -rf ./gopher-http-proxy \
&& mkdir -p /var/cnf

WORKDIR /var/cnf

ENTRYPOINT ["/go/httpproxy"]
