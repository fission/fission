#!/usr/bin/env python

import imp
from http.server import BaseHTTPRequestHandler, HTTPServer

codepath = '/userfunc/user'
userfunc = None

#
# Request context that's passed to user functions
#
class Context:
    def __init__(self, handler):
        self.handler = handler

class testHTTPServer_RequestHandler(BaseHTTPRequestHandler):
    def do_POST(self):
        global userfunc
        userfunc = (imp.load_source('user', codepath)).main
        self.send_response(200)
        self.end_headers()
        self.wfile.write(bytes("ok\n", "utf8"))
        return

    # GET
    def do_GET(self):
        global userfunc
        try:
            print("GET request")
            ctx = Context(self)
            message = userfunc(ctx)
            self.send_response(200)
            self.end_headers()
            self.wfile.write(bytes(message, "utf8"))
            return
        except:
            self.send_response(500)
            self.end_headers()

def run():
    print('starting server...')
    server_address = ('0.0.0.0', 8888)
    httpd = HTTPServer(server_address, testHTTPServer_RequestHandler)
    httpd.serve_forever()


run()
