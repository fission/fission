# frozen_string_literal: true

require_relative 'request'

module Fission
  class Context
    attr_reader :env

    def initialize(env)
      @env = env
    end

    def request
      @request ||= Request.new(env)
    end
  end
end
