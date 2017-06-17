# frozen_string_literal: true
def handler(context)
  request = context.request
  msg = <<~MSG
    ---ENV---
    #{request.env.map { |h| h.join('=') }.join("\n") }

    ---HEADERS---
    #{request.headers.map { |h| h.join(': ') }.join("\n") }

    ---PARAMS---
    #{request.params.map { |h| h.join('=') }.join("\n") }

    --BODY--
    #{request.body.read}
  MSG

  Rack::Response.new([msg]).finish
end
