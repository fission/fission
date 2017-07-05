# frozen_string_literal: true

module Fission
  CODE_PATH = '/userfunc/user'

  module Specializer
    def self.call(env)
      load CODE_PATH
      Rack::Response.new([], 201).finish
    rescue
      Rack::Response.new(['500 Internal Server Error'], 500, {}).finish
    end
  end
end
