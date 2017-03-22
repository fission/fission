FROM golang:1.8

ENV GOPATH /usr
ENV APP	   ${GOPATH}/src/github.com/fission/fission/environments/go

ADD context	    ${APP}/context
ADD server.go   ${APP}

WORKDIR ${APP}
RUN go get
RUN go build -o /server server.go

ENTRYPOINT ["/server"]
EXPOSE 8888
