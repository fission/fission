FROM alpine:3.5

RUN apk update
RUN apk add --no-cache python3 python3-dev build-base
RUN pip3 install --upgrade pip
RUN rm -r /root/.cache

ADD defaultBuildCmd /usr/local/bin/build
ADD builder /

EXPOSE 8000
