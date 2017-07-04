# frozen_string_literal: true
require 'rack'

module Fission
  CODE_PATH = '/userfunc/user'

  HEADER_PREFIX = 'HTTP_'
  PARAM_HEADER_PREFIX = 'HTTP_X_FISSION_PARAMS_'
  PARAMETERS_KEY = 'fission.request.parameters'

  class Request < Rack::Request
    def headers
      Hash[
        *env.select { |k,v| k.start_with?(HEADER_PREFIX) }
             .map { |k,v| [k.sub(/\A#{HEADER_PREFIX}/, '').split('_').map(&:capitalize).join('-'), v] }
             .sort
             .flatten
      ]
    end

    def params
      env[PARAMETERS_KEY] ||= super.merge(path_parameters)
    end

    def path_parameters
      Hash[
        *env.select { |k,v| k.start_with?(PARAM_HEADER_PREFIX) }
             .map { |k,v| [k.sub(/\A#{PARAM_HEADER_PREFIX}/, '').downcase, v] }
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
      load CODE_PATH
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
