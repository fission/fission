#!/usr/bin/env python

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
# TODO: this starts the built-in server, which isn't the most
# efficient.  We should use something better.
#
app.run(host='0.0.0.0', port='8888')
