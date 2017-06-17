FROM ruby:2.4.1

RUN apt-get update -qq && apt-get install -y build-essential

RUN mkdir /app
WORKDIR /app
ADD Gemfile /app/Gemfile
ADD Gemfile.lock /app/Gemfile.lock
RUN bundle install

COPY . /app

EXPOSE 8888

ENTRYPOINT ["ruby"]
CMD ["server.rb"]
