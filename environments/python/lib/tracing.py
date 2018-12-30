from jaeger_client import Config
from flask import request, make_response, g
from opentracing.propagation import Format
import logging

logger = logging.getLogger("lib.tracing")
# Tracer is created globally and initialized only once because of
# following bug.
# https://github.com/jaegertpracing/jaeger-client-python/issues/50
# We won't be calling tracer.close()
tracer = None


def initialize_tracing(func):
    """
    Decorator which initializes the tracing related stuff.
    It creates tracer and a new span.
    Also extracts and injects the headers required for Jaeger tracing
    to work across multiple functions

    :param func: Function object
    :returns: decorated function
    """
    def inner():
        global tracer
        fission_func_name = request.headers.get("X-Fission-Function-Name",
                                                "name")
        span_name = fission_func_name + "-span"
        if tracer is None:
            tracer = _init_tracer(fission_func_name)
        span_ctx = tracer.extract(Format.HTTP_HEADERS, request.headers)
        response = None
        with tracer.start_span(span_name, child_of=span_ctx) as span:
            span.set_tag("generated-by", "lib.tracing")
            generated_headers = dict()
            tracer.inject(span, Format.HTTP_HEADERS, generated_headers)
            # User may want to set tags on span or use the generated_headers
            g.span = span
            g.generated_headers = generated_headers
            # User may return a None, string or object of response
            # Supported types:
            # http://flask.pocoo.org/docs/1.0/api/#flask.Flask.make_response
            func_resp = func()
            if func_resp is None:
                response = make_response()
            else:
                response = make_response(func_resp)
            for key, value in generated_headers.items():
                response.headers[key] = value
        return response
    return inner


def _init_tracer(service):
    """
    This takes a name of service and creates new tracer using
    jaeger_client.
    reporting_host is taken from environment variable
    JAEGER_AGENT_HOST
    reporting_port is taken from environment variable
    JAEGER_AGENT_PORT

    :param service: name of service (string)
    :returns: tracer object
    """
    client_config = Config(
        config={
            "sampler": {"type": "const", "param": 1},
            "logging": True,
        },
        service_name=service,
    )
    return client_config.new_tracer()
