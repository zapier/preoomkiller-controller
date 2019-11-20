
FROM golang:1.13-stretch as builder

# I can't believe we still need this stupid env var for 1.12
ENV GO111MODULE=on

# Setting up sre-tools build
COPY . $GOPATH/src/github.com/zapier/preoomkiller-controller/
WORKDIR $GOPATH/src/github.com/zapier/preoomkiller-controller/
RUN go mod download

# Running sre-tools build
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -a -installsuffix cgo -ldflags="-w -s" -o /go/bin/preoomkiller-controller

# Starting on Scratch
FROM scratch

# Moving needed binaries to
COPY --from=builder /go/bin/preoomkiller-controller /go/bin/preoomkiller-controller

ENTRYPOINT ["/go/bin/preoomkiller-controller"]
