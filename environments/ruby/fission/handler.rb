# frozen_string_literal: true

require_relative 'context'

module Fission
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
