# frozen_string_literal: true

require_relative 'request'
require 'forwardable'

module Fission
  class Context
    extend Forwardable

    def_instance_delegator :request, :logger

    attr_reader :env

    def initialize(env)
      @env = env
    end

    def request
      @request ||= Request.new(env)
    end
  end
end
