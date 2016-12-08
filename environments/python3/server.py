#!/usr/bin/env python

import logging
import sys
import imp

from flask import Flask
from flask import request
from flask import abort

app = Flask(__name__)

codepath = '/userfunc/user'

userfunc = None

@app.route('/specialize', methods=['POST'])
def load():
    global userfunc
    userfunc = (imp.load_source('user', codepath)).main
    return ""

@app.route('/', methods=['GET', 'POST', 'PUT', 'HEAD', 'OPTIONS', 'DELETE'])
def f():
    if userfunc == None:
        print("Generic container: no requests supported")
        abort(500)
    return userfunc()

#
# Logging setup.  TODO: Loglevel hard-coded for now. We could allow
# functions/routes to override this somehow; or we could create
# separate dev vs. prod environments.
#
def setup_logger(loglevel):
    global app
    root = logging.getLogger()
    root.setLevel(loglevel)
    ch = logging.StreamHandler(sys.stdout)
    ch.setLevel(loglevel)
    ch.setFormatter(logging.Formatter('%(asctime)s - %(levelname)s - %(message)s'))
    app.logger.addHandler(ch)

#
# TODO: this starts the built-in server, which isn't the most
# efficient.  We should use something better.
#
setup_logger(logging.DEBUG)
app.logger.info("Starting server")
app.run(host='0.0.0.0', port='8888')
