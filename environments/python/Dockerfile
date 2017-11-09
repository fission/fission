FROM alpine:3.5

RUN apk update
RUN apk add --no-cache python3 python3-dev build-base
RUN pip3 install --upgrade pip
RUN rm -r /root/.cache

COPY . /app
WORKDIR /app
RUN pip3 install -r requirements.txt

ENTRYPOINT ["python3"]
CMD ["server.py"]

