FROM php:7.1-cli

RUN apt-get update && apt-cache search libjpeg && apt-get install -y libjpeg-dev && apt-get install -y \
        libc6 \
        libmysqlclient18 \
        ucf \
        libpq5 \
        libcurl4-openssl-dev \
        libjson-c2 \
        libmcrypt-dev \
        libxpm4 \
        libpq-dev \
        libxml2-dev \
        libgd3 \
        libfreetype6-dev \
        libpng12-dev

RUN docker-php-ext-install curl iconv mcrypt mysqli pdo pdo_mysql phar pgsql pdo_pgsql json opcache ftp simplexml xmlrpc

RUN docker-php-ext-configure gd --with-freetype-dir=/usr/include/ --with-jpeg-dir=/usr/include/ \
        && docker-php-ext-install gd

COPY . /app
WORKDIR /app

EXPOSE 8888

ENTRYPOINT ["php"]
CMD ["-S","0.0.0.0:8888","server.php"]