# frozen_string_literal: true

module Fission
  CODE_PATH = '/userfunc/user'

  module Specializer
    def self.call(env)
      request = Request.new(env)

      load CODE_PATH

      Rack::Response.new([], 201).finish

    rescue => e
      request.logger.error(%(Specialization failed - #{e}\n#{e.backtrace.join("\n")}))
      Rack::Response.new(['500 Internal Server Error'], 500, {}).finish
    end
  end
end
