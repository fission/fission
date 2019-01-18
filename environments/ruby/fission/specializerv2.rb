# frozen_string_literal: true
require 'benchmark'
require 'json'

module Fission

  module SpecializerV2
    def self.call(env)
      request = Request.new(env)
      body    = request.body.string

      request.logger.info("Body #{body}")

      body         = JSON.parse(body)
      filepath     = body['filepath']
      functionname = body['functionName']
      module_name, func_name = functionname.split('.')

      request.logger.info("Codepath defaulting to #{filepath}")

      if(File.directory?(filepath))
        time = Benchmark.measure { Dir["#{filepath}/*.rb"].each {|file| require file } }
      else
        time = Benchmark.measure { load filepath }
      end

      func_name = func_name || "handler"
      alias :"handler" :"#{func_name}"

      request.logger.info("User code loaded in #{(time.real * 1000).round(3)}ms")

      Rack::Response.new([], 202).finish

    rescue => e
      request.logger.error(%(Specialization failed - #{e}\n#{e.backtrace.join("\n")}))
      Rack::Response.new(['500 Internal Server Error'], 500, {}).finish
    end
  end
end
