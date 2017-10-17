# A docker image for the func container.

# debian variant is the official standard node image (larger than the alpine image)
FROM node:8

ARG NODE_ENV
ENV NODE_ENV $NODE_ENV

RUN mkdir -p /usr/src/app
WORKDIR /usr/src/app

COPY package.json /usr/src/app/
RUN npm install && npm cache clean --force
COPY server.js /usr/src/app/server.js

CMD [ "npm", "start" ]

EXPOSE 8888