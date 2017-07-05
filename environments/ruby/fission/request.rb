# frozen_string_literal: true

module Fission
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
end
