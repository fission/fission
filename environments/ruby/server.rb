# frozen_string_literal: true

require 'rack'
require 'logger'
require 'socket'

require_relative 'fission/specializer'
require_relative 'fission/specializerv2'
require_relative 'fission/handler'

fission_port = 8888
address_list = Socket.ip_address_list.map { |address| "http://#{address.ip_address}:#{fission_port}" }
address_list = address_list.push("")

app = Rack::Builder.new do
  use Rack::Logger, Logger::DEBUG

  # Root cause:
  # d23b210 https://github.com/fission/fission/commit/d23b210f4ef6ac8684783b320855445ad1568e4c
  # X-Forwarded-Host header added by router/functionHandler.go:413
  # Requires webrick to explicitly add handler for specific server ip.
  # As pointed in: rack/rack#990 https://github.com/rack/rack/issues/990
  address_list.each do |address|
      map "#{address}/specialize" do
        run Fission::Specializer
      end

      map "#{address}/v2/specialize" do
        run Fission::SpecializerV2
      end

      map "#{address}/healthz" do
        run ->(env) {[200, {'Content-Type' => 'text/html'}, ['']] }
      end

      map "#{address}/readniess-healthz" do
        run ->(env) {[200, {'Content-Type' => 'text/html'}, ['']] }
      end

      map "#{address}/" do
        run Fission::Handler
      end
  end

end

Rack::Handler::WEBrick.run app, Host: '0.0.0.0', Port: fission_port
