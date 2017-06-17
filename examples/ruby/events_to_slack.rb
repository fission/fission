# frozen_string_literal: true
require 'net/http'
require 'uri'
require 'json'

SLACK_BASE_URL = 'https://hooks.slack.com/'
SLACK_WEBHOOK_PATH = 'YOUR RELATIVE PATH HERE' # Something like "/services/XXX/YYY/zZz123"

def send_slack_message(message)
	uri = URI.join(SLACK_BASE_URL, SLACK_WEBHOOK_PATH)
	data = "{'channel': '#hackdays-serverless', 'username': 'fissionbot', 'text': \"#{message}\", 'icon_emoji': ':fission:'}"
	res = Net::HTTP.post_form(uri, payload: data)
	res.success?
end

def handler(context)
	request = context.request

	event_type = request.headers['X-Kubernetes-Event-Type']
	object_type = request.headers['X-Kubernetes-Object-Type']

	object = JSON.parse(request.body.read)
	object_name = object.dig('metadata', 'name')
	object_namespace = object.dig('metadata', 'namespace')

	message = "#{event_type} #{object_type} #{object_namespace}/#{object_name}"

	if send_slack_message(message)
		"Slack message sent - #{message}"
	else
		Rack::Response.new(["Failed to send Slack message - #{message}"], 500).finish
	end
end
