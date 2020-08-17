FROM alpine

WORKDIR /app

RUN apk add --update gcc && \
    apk upgrade --no-cache && \
    apk add --no-cache python3 python3-dev build-base libev-dev libffi-dev bash py-pip && \
    pip3 install --upgrade pip && \
    rm -r /root/.cache

COPY requirements.txt /app
RUN pip3 install -r requirements.txt

COPY . /app

ENTRYPOINT ["python3"]
CMD ["server.py"]
