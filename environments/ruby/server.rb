# frozen_string_literal: true

require 'rack'
require 'logger'

require_relative 'fission/specializer'
require_relative 'fission/handler'

app = Rack::Builder.new do
  use Rack::Logger, Logger::DEBUG

  map "/specialize" do
    run Fission::Specializer
  end

  map "/" do
    run Fission::Handler
  end
end

Rack::Handler::WEBrick.run app, Host: '0.0.0.0', Port: 8888
