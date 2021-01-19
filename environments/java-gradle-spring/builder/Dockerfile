## Fission builder base image
ARG BUILDER_IMAGE=fission/builder:latest
FROM ${BUILDER_IMAGE}

## Section copied from the openjdk:8-jdk-alpine Dockerfile - (https://github.com/docker-library/openjdk/blob/47a6539cd18023dafb45db9013455136cc0bca07/8/jdk/alpine/Dockerfile)

ENV LANG C.UTF-8
RUN { \
    echo '#!/bin/sh'; \
    echo 'set -e'; \
    echo; \
    echo 'dirname "$(dirname "$(readlink -f "$(which javac || which java)")")"'; \
    } > /usr/local/bin/docker-java-home \
    && chmod +x /usr/local/bin/docker-java-home
ENV JAVA_HOME /usr/lib/jvm/java-1.8-openjdk
ENV PATH $PATH:/usr/lib/jvm/java-1.8-openjdk/jre/bin:/usr/lib/jvm/java-1.8-openjdk/bin

ENV JAVA_VERSION 8u181
## Use "fuzzy" version matching to pin the version to a major/minor release
ENV JAVA_ALPINE_VERSION "~8"

RUN set -x \
    && apk add --no-cache \
    openjdk8="$JAVA_ALPINE_VERSION" \
    && [ "$JAVA_HOME" = "$(docker-java-home)" ]

RUN apk add --no-cache curl tar bash procps git

# Section copied from adoptopenjdk:8-jdk-openj9 Dockerfile - (https://github.com/keeganwitt/docker-gradle/blob/c618fa59f4cf872845a6f38bf7cce948fbf8dbc4/openj9/jdk8/Dockerfile)

ENV GRADLE_HOME /opt/gradle
ENV GRADLE_VERSION 6.8
ARG GRADLE_DOWNLOAD_SHA256=e2774e6fb77c43657decde25542dea710aafd78c4022d19b196e7e78d79d8c6c
RUN set -o errexit -o nounset \
    && echo "Downloading Gradle" \
    && wget --no-verbose --output-document=gradle.zip "https://services.gradle.org/distributions/gradle-${GRADLE_VERSION}-bin.zip" \
    \
    && echo "Checking download hash" \
    && echo "${GRADLE_DOWNLOAD_SHA256} *gradle.zip" | sha256sum -c - \
    \
    && echo "Installing Gradle" \
    && unzip gradle.zip \
    && rm gradle.zip \
    && mv "gradle-${GRADLE_VERSION}" "${GRADLE_HOME}/" \
    && ln -s "${GRADLE_HOME}/bin/gradle" /usr/bin/gradle \
    \
    && echo "Testing Gradle installation" \
    && gradle --version

## Fission builder specific section
ADD build.sh /usr/local/bin/build
EXPOSE 8001
