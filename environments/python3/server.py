#!/usr/bin/env python

import imp
from http.server import BaseHTTPRequestHandler, HTTPServer

#
# Load the file.
#
codepath = '/userfunc/user'
userfunc = None

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
            message = userfunc(None)
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
