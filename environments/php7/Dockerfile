FROM php:7.1-cli

ENV PATH="/root/.composer/vendor/bin:${PATH}" \
    COMPOSER_ALLOW_SUPERUSER=1

RUN apt-get update && apt-get install -y \
        libc6 \
        libjpeg-dev \
        libmysqlclient18 \
        ucf \
        git \
        zip \
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

RUN curl -sS https://getcomposer.org/installer | php -- --install-dir=/usr/local/bin --filename=composer

COPY . /app
WORKDIR /app

RUN composer install

EXPOSE 8888

ENTRYPOINT ["php"]
CMD ["-S","0.0.0.0:8888","index.php"]