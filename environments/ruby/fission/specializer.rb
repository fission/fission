# frozen_string_literal: true
require 'benchmark'

module Fission
  CODE_PATH = '/userfunc/user'

  module Specializer
    def self.call(env)
      request = Request.new(env)

      request.logger.info("Codepath defaulting to #{CODE_PATH}")
      time = Benchmark.measure { load CODE_PATH }
      request.logger.info("User code loaded in #{(time.real * 1000).round(3)}ms")

      Rack::Response.new([], 201).finish

    rescue => e
      request.logger.error(%(Specialization failed - #{e}\n#{e.backtrace.join("\n")}))
      Rack::Response.new(['500 Internal Server Error'], 500, {}).finish
    end
  end
end
