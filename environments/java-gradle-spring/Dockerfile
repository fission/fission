FROM gradle:6.7-jdk8-openj9 as builder

WORKDIR /usr/src/myapp/

COPY build.gradle .
COPY settings.gradle .

COPY src /usr/src/myapp/src/

RUN gradle build

# get gradle:6.7-jdk8-openj9 base image
FROM adoptopenjdk:8-jdk-openj9

VOLUME /tmp

COPY --from=builder /usr/src/myapp/build/libs/env-java-gradle-spring-0.0.1-SNAPSHOT.jar /app.jar

ENTRYPOINT java ${JVM_OPTS} -Djava.security.egd=file:/dev/urandom -jar /app.jar --server.port=8888

EXPOSE 8888
