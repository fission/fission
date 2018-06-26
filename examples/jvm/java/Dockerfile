FROM maven:3.5-jdk-8 as BUILD
COPY src /usr/src/myapp/src
COPY pom.xml /usr/src/myapp
ENTRYPOINT mvn -f /usr/src/myapp/pom.xml clean package