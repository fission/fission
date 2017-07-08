# frozen_string_literal: true
def handler(context)
  context.logger.info("Received request")

  msg = <<~MSG
    ---ENV---
    #{context.request.env.map { |h| h.join('=') }.join("\n") }

    ---HEADERS---
    #{context.request.headers.map { |h| h.join(': ') }.join("\n") }

    ---PARAMS---
    #{context.request.params.map { |h| h.join('=') }.join("\n") }

    --BODY--
    #{context.request.body.read}
  MSG

  Rack::Response.new([msg]).finish
end
