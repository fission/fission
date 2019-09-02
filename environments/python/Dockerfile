FROM alpine:3.5

WORKDIR /app

RUN apk add --no-cache python3 python3-dev build-base libev-dev && \
    pip3 install --upgrade pip && \
    rm -r /root/.cache

COPY requirements.txt /app
RUN pip3 install -r requirements.txt

COPY . /app

ENTRYPOINT ["python3"]
CMD ["server.py"]
