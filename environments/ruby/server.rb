# frozen_string_literal: true
require 'rack'

CODEPATH = '/userfunc/user'

module Fission
  class Request < Rack::Request
    def headers
      Hash[
        *env.select { |k,v| k.start_with?('HTTP_') }
             .map { |k,v| [k.sub(/\AHTTP_/, '').split('_').map(&:capitalize).join('-'), v] }
             .sort
             .flatten
      ]
    end
  end

  class Context
    attr_reader :env

    def initialize(env)
      @env = env
    end

    def request
      @request ||= Request.new(env)
    end
  end

  module Specializer
    def self.call(env)
      load CODEPATH
      Rack::Response.new([], 201).finish
    rescue
      Rack::Response.new(['500 Internal Server Error'], 500, {}).finish
    end
  end

  module Handler
    def self.call(env)
      response = if method(:handler).arity > 0
        handler(Context.new(env))
      else
        handler
      end

      response.is_a?(Array) ? response : Rack::Response.new([response]).finish
    rescue
      Rack::Response.new(['500 Internal Server Error'], 500, {}).finish
    end
  end
end

app = Rack::Builder.new do
  use Rack::CommonLogger, $stderr

  map "/specialize" do
    run Fission::Specializer
  end

  map "/" do
    run Fission::Handler
  end
end

Rack::Handler::WEBrick.run app, Host: '0.0.0.0', Port: 8888
